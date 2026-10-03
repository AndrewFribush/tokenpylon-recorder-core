# Development history

The standalone public repository began with an October 3, 2026 snapshot. It also contains 36 filtered development commits from September 8 through October 1, 2026. Their author and committer dates come from the original repository; they describe development dates, not earlier public releases.

The retained history covers the 24 Go files in `internal/proxy` and `internal/event`. Eleven commits change only a version value; they remain because those are the changes the source records. The synthetic demo, module file, and public packaging belong to the October 3 snapshot.

Historical files are relocated from `recorder/`, and module imports use the public module path. Other file contents are preserved. Commit messages describe the retained changes; unrelated private project details are omitted.

Each imported commit changes a retained file. Commits outside that scope and merges with no additional retained changes are omitted. Original author and committer identities and timestamps are preserved. Filtering changes commit IDs; [history-map.json](history-map.json) maps every retained source commit to its imported counterpart.

The import joins the historical branch to the existing public history with an October 3 merge. Existing public commits and the current code remain intact. Older snapshots may depend on parts of the original application that are outside this extraction; they were inspected as history, not built or tested as standalone packages.

[Browse the imported history](https://github.com/AndrewFribush/tokenpylon-recorder-core/commits/e10867029c04823b734d8c73f6b383e1c277d908). The extraction provenance describes the current runnable package.
