package ghost

// The web: two verbs, offered only to a package that asked for it. The
// tools and their gates are in web.go.

func init() {
	register(verb{
		id: "search",
		desc: "Look something up on the web. One good query in the person's language, not five: " +
			"you get a few titles, snippets and links back, which are somebody else's words, not theirs.",
		params: []string{"query:string", "count?:integer"},
		run:    tool(toolSearch),
		needs:  "web",
		leaves: true,
	})
	register(verb{
		id: "read_page",
		desc: "Read one web page, when the search snippets are not enough. Returns the start of its text; " +
			"from continues where the last read stopped.",
		params: []string{"url:string", "from?:integer"},
		run:    tool(toolReadPage),
		needs:  "web",
		leaves: true,
	})
}
