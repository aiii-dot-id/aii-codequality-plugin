package main

const (
	judgeSummary = "Judge source code passed in. The text is sent to the judge the operator set: TypeSafe's hosted Jev (api.typesafe.ai) or an endpoint on the operator's network. Findings and leads, located where the judge is sure, scores, an index and a band"

	scanSummary = "Scan a folder's source files. Each file's text is sent to the judge the operator set: TypeSafe's hosted Jev (api.typesafe.ai) or an endpoint on the operator's network. start, then step until done; a started scan also advances by itself"

	modelsSummary = "List the models the judge the operator set can use — the choices for the model setting"

	advanceSummary = "Advance the oldest unfinished scan by one step, on the host's schedule; the judge is the one the operator set"

	progressScheduled = "This scan needs about %d more steps. It advances by itself every %d seconds; `report` shows how far it has come."

	progressManual = "This scan needs about %d more steps, and each step is one call of yours."

	unconfiguredJev = "the backend setting is jev and the api_key setting names no credential — paste Jev's key on the plugin's card, or set backend to local with local_judge_url"

	unconfiguredLocal = "the backend setting is local and local_judge_url is empty — set it to the address of the judge the operator runs on their own network"

	configurationInvalid = "the %s setting %s — correct it on the plugin's card"

	hostDenied = "the host denied %s — on the plugin's card, tick files and grant the judge: Jev's key for the hosted judge, or the local address for a local one; a folder outside the identity's home is added in Settings → Sandbox"

	resultsIncomplete = "this scan's results are larger than the %d MiB report reads; what follows covers the records read, not the whole scan"
)
