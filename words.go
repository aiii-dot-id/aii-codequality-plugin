package main

const (
	judgeSummary = "Judge source code passed in. The text is sent to the judge the operator set: TypeSafe's hosted Jev (api.typesafe.ai) or an endpoint on the operator's network. Model allegations per window, each with its assessment; not verified defects"

	scanSummary = "Scan a folder's source files. Each file's text is sent to the judge the operator set: TypeSafe's hosted Jev (api.typesafe.ai) or an endpoint on the operator's network. start, then step until done; report lists files to review and coverage gaps"

	reportSummary = "Report distinct files to review and coverage gaps; summary retains legacy chunk scores. Findings are model allegations, not verified defects. No scan_id lists scans"

	modelsSummary = "List the models the judge the operator set can use — the choices for the model setting"

	advanceSummary = "Advance the oldest unfinished scan by one step, on the host's schedule; the judge is the one the operator set"

	progressScheduled = "This scan needs about %d more steps. It advances by itself every %d seconds; `report` shows how far it has come."

	progressManual = "This scan needs about %d more steps, and each step is one call of yours."

	unconfiguredJev = "the backend setting is jev and the api_key setting names no credential — paste Jev's key on the plugin's card, or set backend to local with local_judge_url"

	unconfiguredLocal = "the backend setting is local and local_judge_url is empty — set it to the address of the judge the operator runs on their own network"

	configurationInvalid = "the %s setting %s — correct it on the plugin's card"

	hostDenied = "the host denied %s — on the plugin's card, tick files and grant the judge: Jev's key for the hosted judge, or the local address for a local one; a folder outside the identity's home is added in Settings → Sandbox"

	judgeKeyRefused = "the judge refused this plugin's credentials (%s) — paste a key that works on the plugin's card, or let the local judge accept this plugin's requests; the scan keeps its place and the next step continues"

	reportIncomplete = "this scan's results are larger than the %d MiB a report ranks; what follows covers the records read, not a repository-wide ranking. Start a narrower scan; durable results are retained, and no omitted file is declared safe."

	refusedNote = "the judge refused %d files, which are not assessed; %d were judged. A 403 page or other refusal is read as refusing that text, not the key: if the judge refused nearly every file, check the key on the plugin's card and the judge's own logs"

	hostClockMissing = "the host supplied no clock for this invocation; persistent retry timing and scan names need one — update AII OS"

	scanUnreadable = "this scan's saved state cannot be read, so it can be neither stepped nor reported; start a new scan. Retention removes it in its turn among the keep_scans newest, sooner if keep_scans is lowered"

	judgeItemTooLarge = "item %q: %d UTF-8 bytes exceeds max_file_bytes %d; pass shorter text"

	judgeTooManyCalls = "these texts need up to %d judge calls; one invoke makes at most %d (max_calls_per_step), and a text needs up to three calls per %d bytes (chunk_bytes), so one text can be at most %d bytes — pass fewer or shorter texts, or scan a larger file"

	checkpointRecovered = "Recovered the last valid checkpoint after an incomplete write; durable journal records will be replayed. "

	legacyReportOnly = "legacy scan is report-only; start a new scan"

	legacyScanRefusal = "legacy scan remains reportable; start a new scan for contextual v2 assessment"

	savedCapUnsupported = "saved scan file cap is outside this release support; retained results remain reportable; start a new scan within max_file_bytes bounds"

	checkpointFull = "pending walk exceeds bounded checkpoint storage; discovery incomplete; start a narrower scan"

	journalFull = "assessment journal reached its storage bound; retained results remain reportable; start a narrower scan"

	scanBlocked = "%s; retained results remain reportable; start a new scan after correcting the cause"

	scanTooWide = "initial scan settings exceed checkpoint budget"

	advanceNone = "no eligible unfinished scan"

	laneInvalid = "lane must be correctness or maintainability"
)
