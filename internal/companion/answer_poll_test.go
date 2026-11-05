package companion

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

type answerPollFleet struct{ Fleet }

func (answerPollFleet) Publish(model.BusEvent) {}

func TestAnswerKeepsExchangeBeforePollOutcome(t *testing.T) {
	for _, mode := range []string{"poll-before-answer", "poll-during-answer", "stale-poll-after-answer"} {
		t.Run(mode, func(t *testing.T) {
			answerReceived, releaseAnswer := make(chan struct{}), make(chan struct{})
			pollReceived, releasePoll := make(chan struct{}), make(chan struct{})
			// Cleanup also unblocks handlers if an assertion fails.
			defer func() {
				select {
				case <-releaseAnswer:
				default:
					close(releaseAnswer)
				}
				select {
				case <-releasePoll:
				default:
					close(releasePoll)
				}
			}()
			remote := Task{ID: "remote"}
			remote.Status.State = StateCompleted
			remote.Status.Message = &Message{Parts: []Part{{Text: "Tomorrow is free."}}}
			remote.History = []Message{{Role: "ROLE_AGENT", Parts: []Part{{Text: "Tomorrow is free."}}}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					close(answerReceived)
					<-releaseAnswer
					result := remote
					if mode == "stale-poll-after-answer" {
						result.Status.State = StateInputRequired
						result.Status.Message = &Message{Parts: []Part{{Text: "Which time?"}}}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"task": result})
				} else {
					close(pollReceived)
					<-releasePoll
					_ = json.NewEncoder(w).Encode(remote)
				}
			}))
			t.Cleanup(server.Close)
			paths := config.Paths{Home: t.TempDir()}
			if err := config.Save(paths, config.Config{Companion: &config.CompanionConfig{URL: server.URL}}); err != nil {
				t.Fatal(err)
			}
			st, err := store.Open(paths.DB())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			s := New(Options{Store: st, Fleet: answerPollFleet{}, Paths: paths})
			t.Cleanup(s.Close)
			task := model.CompanionTask{ID: "local", RemoteID: remote.ID, Kind: model.CompanionKindMessage,
				Origin: "owner", State: model.CompanionInputRequired, Question: "Which day?",
				CreatedAt: 1, UpdatedAt: 2, Exchanges: []model.CompanionExchange{{Question: "Which day?", AskedAt: 2}}}
			if err := st.PutCompanionTask(task); err != nil {
				t.Fatal(err)
			}
			wait := func(ch <-chan struct{}) {
				t.Helper()
				select {
				case <-ch:
				case <-time.After(5 * time.Second):
					t.Fatal("timed out waiting for request")
				}
			}
			pollDone := make(chan struct{})
			poll := func() { s.poll(task); close(pollDone) }
			if mode != "poll-during-answer" {
				go poll()
				wait(pollReceived)
			}
			answerDone := make(chan error, 1)
			go func() { answerDone <- s.answer(task, "Tomorrow", "Tomorrow", model.Owner) }()
			wait(answerReceived)
			if mode == "poll-during-answer" {
				close(releasePoll)
				go poll()
			} else if mode == "poll-before-answer" {
				close(releasePoll)
			}
			if mode != "stale-poll-after-answer" {
				wait(pollDone)
			}
			close(releaseAnswer)
			select {
			case err := <-answerDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("answer did not finish")
			}
			if mode == "stale-poll-after-answer" {
				close(releasePoll)
				wait(pollDone)
			}
			got, err := s.Get(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Exchanges) == 0 || got.Exchanges[0].Answer != "Tomorrow" || got.Exchanges[0].AnsweredBy != model.Owner {
				t.Fatalf("accepted answer lost: %+v", got)
			}
			if mode == "stale-poll-after-answer" {
				if got.State != model.CompanionInputRequired || got.Question != "Which time?" || len(got.Exchanges) != 2 {
					t.Fatalf("stale poll overwrote the next question: %+v", got)
				}
			} else if got.State != model.CompanionCompleted || got.Result != "Tomorrow is free." || got.Exchanges[0].AnsweredAt > got.FinishedAt {
				t.Fatalf("answer must precede completion: %+v", got)
			}
		})
	}
}
