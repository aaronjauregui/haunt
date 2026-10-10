package ghost

// The person's files: three read-only verbs, offered only to a package
// that asked for them. None of them is a side effect or leaves the
// machine: what they add to a turn is taint. The tools and their gates
// are in files.go.

func init() {
	register(verb{
		id: "list_files",
		desc: "See what is in the folders the person shared with you. No path lists the folders; " +
			"a path like notes/2026 lists what is in it.",
		params: []string{"path?:string"},
		run:    tool(toolListFiles),
		needs:  "files",
	})
	register(verb{
		id: "read_file",
		desc: "Read one file from a shared folder: text, a PDF, or a picture (described to you). " +
			"Returns a piece at a time; from continues where the last read stopped. question is for pictures.",
		params: []string{"path:string", "from?:integer", "question?:string"},
		run:    tool(toolReadFile),
		needs:  "files",
	})
	register(verb{
		id: "search_files",
		desc: "Find files by name or by words inside them, in the shared folders or under path. " +
			"Case does not matter. Each hit gives a from to pass to read_file.",
		params: []string{"text:string", "path?:string", "names_only?:boolean"},
		run:    tool(toolSearchFiles),
		needs:  "files",
	})
}
