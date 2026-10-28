package cq

const (
	locateNone = "No valid single unit: the alleged fault is absent, distributed across units, or the evidence is insufficient."

	locateQuestion = "In `units`: which single unit contains the place where %s? Choose cannot_localize if none is justified; selecting a location does not verify the fault."

	questionLines = "Original-source lines %d–%d. %s"

	reviewCaveat = "Model allegations, not confirmed defects. Severity/category ordering is an unvalidated candidate. Legacy scores are descriptive, not safety verdicts. Historical calibration is not current-model accuracy."

	reviewNextCheck = "Inspect the cited source with callers, ownership contracts and tests; confirm the consequence or reject the allegation."

	reviewSelection = "%s candidate: maximum %s catalogue severity; %d distinct flagged categories; assessment %s. Category breadth is not a defect count."

	scoreCaveat = "Legacy index/bands describe maintainability arithmetic, not correctness. Worst entries are chunks, not distinct files; inspect assessment and coverage separately."

	staleCursor = "review cursor belongs to different scan evidence or query; restart at the first page"

	reviewEntryTooLarge = "one file review exceeds page_bytes; use a larger page or findings shape for its individual chunk evidence"

	pageBoundsInvalid = "page_bytes must be 4096..200000 and limit 1..100"

	rulesRetry = " — rules must validate before retry"
)
