package main

const (
	judgeSummary = "Judge source code passed in. The text is sent to the judge the operator set: TypeSafe's hosted Jev (api.typesafe.ai) or an endpoint on the operator's network. Model allegations per window, each with its assessment; not verified defects"

	scanSummary = "Scan a folder's source files. Each file's text is sent to the judge the operator set: TypeSafe's hosted Jev (api.typesafe.ai) or an endpoint on the operator's network. start, then step until done; report lists files to review and coverage gaps"

	reportSummary = "Report distinct files to review and coverage gaps; summary retains legacy chunk scores. Findings are model allegations, not verified defects. No scan_id lists scans"

	modelsSummary = "List the models the judge the operator set can use — the choices for the model setting"

	advanceSummary = "Advance the oldest unfinished scan by one step, on the host's schedule; the judge is the one the operator set"

	progressScheduled = "This scan is unfinished and advances by itself; `report` shows how far it has come."

	progressManual = "This scan is unfinished, and each step is one call of yours."

	callsBeforeCaching = "before caching, %d judge calls for each started %d KiB of a file's source (the code, where a finding is, the comments, and the slop questions), the figure each step budgets by; one more for a file with more than one top-level function (its outline), and one more for each window with an ignored-error flag (the intent question); a window already cached costs none"

	slopBeforeR2 = "This scan was made before 0.2.0-r2: it has no size, slop or outline data, so none is shown. A rescan records them."

	unconfiguredJev = "the backend setting is jev and the api_key setting names no credential — paste Jev's key on the plugin's card, or set backend to local with local_judge_url"

	unconfiguredLocal = "the backend setting is local and local_judge_url is empty — set it to the address of the judge the operator runs on their own network"

	configurationInvalid = "the %s setting %s — correct it on the plugin's card"

	hostDenied = "the host denied %s — on the plugin's card, tick files and grant the judge: Jev's key for the hosted judge, or the local address for a local one; a folder outside the identity's home is added in Settings → Sandbox"

	judgeKeyRefused = "the judge refused this plugin's credentials (%s) — paste a key that works on the plugin's card, or let the local judge accept this plugin's requests; the scan keeps its place and the next step continues"

	reportIncomplete = "this scan's results are larger than the %d MiB a report ranks; what follows covers the records read, not a repository-wide ranking. Start a narrower scan; durable results are retained, and no omitted file is declared safe."

	refusedNote = "the judge refused %d files, which are not assessed; %d were judged. A 403 page or other refusal is read as refusing that text, not the key: if the judge refused nearly every file, check the key on the plugin's card and the judge's own logs"

	notFinishedWords = "not assessed: the plugin's worker was ended %d times while working on each of these files, out of memory or past a step's time limit, so the scan went on without them. Such a file is usually one very long function or literal, or tens of thousands of one-line declarations. A new scan tries them again"

	tooWideWords = "not assessed: listing each of these folders would leave more files and folders to visit than the scan's saved state has room for (%d KiB of names and sizes), so the scan went on without the folder and everything under it. Scan such a folder's subfolders apart, or exclude some of its files"

	listingTruncatedWords = "not assessed: each of these folders holds more entries than the plugin lists for one folder (it reads at most %d pages of a folder's listing, 1,024 entries a page on AII OS 0.2.2), so the scan went on without the folder and everything under it. Scan such a folder's subfolders apart"

	planTooLargeWords = "not assessed: each of these files is larger than one segment, and the plan the scan keeps to judge such a file a segment at a time (its segments, where its declarations begin and an outline of its functions and types) is more than the %d KiB the scan's saved state has room for, so the scan went on without them. Such a file is usually hundreds of functions whose signatures are full of angle brackets or ampersands"

	hostClockMissing = "the host supplied no clock for this invocation; persistent retry timing and scan names need one — update AII OS"

	scanUnreadable = "this scan's saved state cannot be read, so it can be neither stepped nor reported; start a new scan. It is kept until keep_scans is set, and a start then removes it in its turn, oldest first"

	judgeItemTooLarge = "item %q: %d UTF-8 bytes exceeds the %d bytes one judge call carries in its invocation frame. That is a limit of one invocation, not a file skip: a scan reads a file of any size, a segment at a time"

	judgeTooManyCalls = "these texts need up to %d judge calls; one invoke makes at most %d (max_calls_per_step), and a text needs up to three calls per %d bytes (chunk_bytes), so one text can be at most %d bytes — pass fewer or shorter texts, or scan a larger file"

	checkpointRecovered = "Recovered the last valid checkpoint after an incomplete write; durable journal records will be replayed. "

	cacheUnreadable = "%s, so the judge was not asked for what that lookup covers and nothing was recorded for it: asking without the cache could pay again for an answer already kept. Anything already answered and recorded for the file stands. A scan keeps the file pending, and its next step goes on from there"

	legacyReportOnly = "legacy scan is report-only; start a new scan"

	legacyScanRefusal = "legacy scan remains reportable; start a new scan for contextual v2 assessment"

	savedCapUnsupported = "saved scan file cap is outside this release support; retained results remain reportable; start a new scan within max_file_bytes bounds"

	pageBytesExceeded = "this page's one record is %d bytes, over page_bytes %d: it is shown whole, since a record is not cut (at most 250 KiB)"
	checkpointFull    = "the paths still to visit at %q do not fit the scan's 250 KiB checkpoint; discovery incomplete. Scan that folder's subfolders apart, or exclude some of its files"

	intentFull = "the intent answers' file reached its share of the scan's room; retained results remain reportable; start a narrower scan"

	intentUnreadSummary = "The intent answers could not be read (%s); top_correctness treats every finding's intent as unknown, so none is left out for it."

	journalFull = "assessment journal reached its storage bound; retained results remain reportable; start a narrower scan"

	scanBlocked = "%s; retained results remain reportable; start a new scan after correcting the cause"

	scansUnlistable = "the plugin's own files could not be listed whole, so what it keeps is not known; nothing was started, stepped or removed. Its folder holds more files than one listing returns"

	scanTooWide = "initial scan settings exceed checkpoint budget"

	scanRoomFull = "there is too little room for a new scan's results (%d KiB more is needed), so no scan was started and nothing was removed: a scan is removed only when it expires, or when you remove it (scan action remove). "

	scanFilesFull = "a new scan's files would leave the plugin's scans folder with more files than the %d one listing returns, so no scan was started and nothing was removed. "

	scanKept = "It keeps: scans, %d (%d KiB); answers from before this version, %d KiB; newer answers, %d KiB; against the %d MiB the plugin plans with. The scans, soonest to expire first:"

	scanHeldLine = " %s (%d KiB, expires %s);"

	scanHeldMore = " and %d more."

	scopeLine = "A file is judged when it is text, in a recognised language, written by a person (not generated or minified, unless include_generated), and at most 1 MiB. Anything else is listed out of scope with its reason and costs no call."

	keepScansRetired = "keep_scans is set to %d on this plugin's card. This version ignores it: scans are kept until they expire (scan_retention_days). Clear it, because an earlier version of this plugin would still remove scans by it, in the order of their names."

	expiryUnknown = "unknown: this scan's files carry no readable time, so it does not expire"

	expiryNoSettings = "unknown: the settings could not be read, and no scan expires until they can"

	scanRemoveNone    = "scan remove requires scan_ids: the scans to remove, by id"
	scanRemoveTwice   = "scan_ids names %s twice"
	scanRemoveUnknown = "no scan named %s; nothing was removed (report with no scan_id lists the scans)"

	cacheRoomHeld      = " cache_max_bytes holds back %d KiB for newer answers; a smaller value holds back less."
	earlierAnswersStay = " No setting removes the answers from before this version."

	advanceNone = "no eligible unfinished scan"

	laneInvalid = "lane must be correctness, maintainability, size or slop"
)
