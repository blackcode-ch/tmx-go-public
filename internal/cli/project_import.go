package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/blackcode-ch/tmx-go/internal/client"
	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"io"
	"os"
)

func cmdProjectImport(e *env, args []string) error {
	fs := newFlags("project import")
	file := fs.String("file", "", "normalized migration JSON file")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *file == "" {
		return usageErr("usage: tmx project import PROJECT --file FILE")
	}
	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 2<<20 {
		return fmt.Errorf("import exceeds 2 MiB")
	}
	var req fleet.ProjectImportRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return err
	}
	return withOwner(e, func(c *client.Client, cx context.Context) error {
		var out model.ProjectImportReceipt
		if err := c.Post(cx, "/api/projects/"+client.Seg(pos[0])+"/import", req, &out); err != nil {
			return err
		}
		return json.NewEncoder(e.out).Encode(out)
	})
}
