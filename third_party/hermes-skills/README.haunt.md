# Hermes's bundled skills, the few a ghost may use

Four skills copied unchanged from hermes-agent's `skills/` directory at
commit 6005aa1 (NousResearch/hermes-agent, MIT, `LICENSE`). `humanizer`
carries its own upstream license beside it.

Ghost profiles opt out of hermes's bundled library (`.no-bundled-skills`),
because nearly all of it assumes a shell, files or the web
(docs/hermes-brain.md, section 14). A ghost's manifest may name any of
these under `[hermes] bundled_skills`, and haunt installs them read-only
into the profile, like its own skills.

They are vendored because hermes at the pinned commit has no way to seed
one bundled skill into an opted-out profile: `hermes skills reset <name>
--restore` seeds only the essential skills there, and `hermes skills
install` fetches the live repo over the network.

Only `SKILL.md` files are embedded. Some text still names tools a ghost
does not have (`read_file`, a calendar or notes connector); the model gets
no such tool and works from what the person pastes or says.

To refresh after moving the hermes pin, copy each directory again from
`skills/<category>/<name>/` in the new source and read the diff.
