package main

import (
	"encoding/json"
	"io"
	"os"
	"strconv"
)

func main() {
	if len(os.Args) > 2 && os.Args[1] == "auth" && os.Args[2] == "status" {
		_, _ = io.WriteString(os.Stdout, `{"loggedIn":true,"authMethod":"oauth_token","apiProvider":"firstParty"}`)
		return
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(2)
	}
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		Input string   `json:"input"`
		Args  []string `json:"args"`
	}{string(data), os.Args[1:]}); err != nil {
		os.Exit(2)
	}
	code, _ := strconv.Atoi(os.Getenv("TASK_FIXTURE_EXIT"))
	os.Exit(code)
}
