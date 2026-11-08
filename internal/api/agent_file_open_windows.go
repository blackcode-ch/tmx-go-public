package api

import "os"

func openAgentFile(root *os.Root, name string) (*os.File, error) { return root.Open(name) }
