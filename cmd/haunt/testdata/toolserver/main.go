// A minimal MCP tool server for the agency tests and the evals: it offers
// a tool that appends a line to a file, so a test can check that the ghost
// really did something in the world rather than only saying it had, and
// one that reads the file back, so an eval can put words in front of the
// ghost that came from a tool rather than from the person.
//
// Everything it writes goes to the file named by HAUNT_TEST_NOTES, which is
// also how the test checks that the driver passes named environment
// variables through and no others.
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := dec.Decode(&req); err != nil {
			return
		}
		if req.ID == nil {
			continue // a notification
		}
		switch req.Method {
		case "initialize":
			reply(enc, *req.ID, map[string]any{"protocolVersion": "2024-11-05"})
		case "tools/list":
			reply(enc, *req.ID, map[string]any{"tools": []map[string]any{{
				"name":        "append",
				"description": "Append a line to the notes file",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
					"required":   []string{"text"},
				},
			}, {
				"name":        "read",
				"description": "Read the whole notes file",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
			}}})
		case "tools/call":
			reply(enc, *req.ID, call(req.Params))
		default:
			enc.Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID,
				"error": map[string]any{"code": -32601, "message": "no such method"}})
		}
	}
}

func call(params json.RawMessage) map[string]any {
	var c struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	json.Unmarshal(params, &c)
	text, _ := c.Arguments["text"].(string)

	path := os.Getenv("HAUNT_TEST_NOTES")
	if path == "" {
		return content("HAUNT_TEST_NOTES did not reach the server", true)
	}
	if c.Name == "read" {
		b, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return content(err.Error(), true)
		}
		return content(string(b), false)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return content(err.Error(), true)
	}
	defer f.Close()
	fmt.Fprintln(f, text)
	// What the driver did *not* pass through is as much of the contract as
	// what it did, so the server reports it: HAUNT_TEST_SECRET is in the
	// driver's own environment and is not named by the manifest.
	if _, leaked := os.LookupEnv("HAUNT_TEST_SECRET"); leaked {
		fmt.Fprintln(f, "leaked: HAUNT_TEST_SECRET")
	}
	return content("appended "+text, false)
}

func content(text string, isErr bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	}
}

func reply(enc *json.Encoder, id int, result any) {
	enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}
