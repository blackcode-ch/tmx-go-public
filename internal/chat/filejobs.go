package chat

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/blackcode-ch/tmx-go/internal/jobs"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/p2p"
)

// filePipe couples the single admitted audit worker to one bounded network
// producer. Closing it cancels Fetch and unblocks its pipe writes before waiting
// for that owned goroutine. No shared chat lock is held across either operation.
type filePipe struct {
	*io.PipeReader
	cancel context.CancelFunc
	done   <-chan struct{}
	file   *os.File
}

func (p *filePipe) Close() error {
	p.cancel()
	p.PipeReader.Close()
	<-p.done
	return p.file.Close()
}

func (s *Service) fetchGoverned(f model.ChatFile, peer string) (bool, string) {
	// The daemon chooses background priority and the local owner. Peer identity
	// comes from the authenticated chat membership path, never agent-supplied jobs.
	identity := jobs.Identity{Caller: "chat-peer:" + peer, Owner: model.Owner, Key: "download/" + f.ChatID + "/" + f.ID}
	var tmp *os.File
	task := jobs.AuditTask{
		Peer: peer, Chat: f.ChatID,
		ExpectedDigest: f.SHA256,
		Open: func(jobctx context.Context) (io.ReadCloser, error) {
			if err := s.ctx.Err(); err != nil {
				return nil, err
			}
			dir := s.fileDir(f.ChatID, f.ID)
			if err := os.MkdirAll(dir, 0700); err != nil {
				return nil, err
			}
			var err error
			tmp, err = os.CreateTemp(dir, ".part-*")
			if err != nil {
				return nil, err
			}
			ctx, cancel := context.WithTimeout(jobctx, fetchTimeout)
			pr, pw := io.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() {
					if recover() != nil {
						pw.CloseWithError(errors.New("fetch worker panic"))
					}
				}()
				// One pass: the network's existing 64 KiB chunks write to disk and then
				// block behind the hash worker. The pipe adds no unbounded byte queue.
				n, err := s.node.Fetch(ctx, peer, f.ChatID, f.ID, io.MultiWriter(tmp, pw), f.Size)
				if errors.Is(err, p2p.ErrUnreachable) || errors.Is(err, p2p.ErrNoFile) {
					err = jobs.ErrDeferred
				}
				if err == nil && n != f.Size {
					err = io.ErrUnexpectedEOF
				}
				pw.CloseWithError(err)
			}()
			return &filePipe{pr, cancel, done, tmp}, nil
		},
		Commit: func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			// Serialize the final membership check and atomic placement against
			// Leave/RemoveMember. All bulk reads/writes and hashing happened before
			// this short final metadata transition.
			s.mu.Lock()
			defer s.mu.Unlock()
			ch, err := s.st.GetChat(f.ChatID)
			if err != nil || ch.Status != model.ChatActive {
				return jobs.ErrForbidden
			}
			if ch.Kind == model.ChatDM {
				if ch.PeerID != peer {
					return jobs.ErrForbidden
				}
			} else {
				if _, ok := s.memberLocked(ch.ID, peer); !ok {
					return jobs.ErrForbidden
				}
			}
			// This only places inert chat bytes. Existing sender/member and serving
			// authorization is unchanged; no approval or execution authority is granted.
			return os.Rename(tmp.Name(), s.filePath(f))
		},
		Cleanup: func() {
			if tmp != nil {
				tmp.Close()
				os.Remove(tmp.Name())
			}
		},
	}
	r, err := s.o.Jobs.SubmitTask(identity, false, f.Size, task)
	switch {
	case errors.Is(err, jobs.ErrQuarantined):
		return false, "quarantined"
	case err != nil:
		return false, "unreachable"
	}
	r, err = s.o.Jobs.Wait(s.ctx, r.ID, model.Owner, true)
	if err != nil {
		return false, "unreachable"
	}
	switch r.State {
	case "completed":
		st, err := os.Stat(s.filePath(f))
		if err == nil && st.Size() == f.Size {
			return true, ""
		}
		return false, "no file"
	case "quarantined":
		return false, "quarantined"
	case "cancelled":
		return false, "cancelled"
	case "revoked":
		return false, "revoked"
	case "paused":
		return false, "unreachable"
	default:
		return false, "damaged"
	}
}
