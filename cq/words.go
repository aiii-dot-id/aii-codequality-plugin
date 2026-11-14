package cq

const (
	locateNone = "No valid single unit: the alleged fault is absent, distributed across units, or the evidence is insufficient."

	locateQuestion = "In `units`: which single unit contains the place where %s? Choose cannot_localize if none is justified; selecting a location does not verify the fault."

	questionLines = "Original-source lines %d–%d. %s"

	reviewCaveat = "Model allegations, not confirmed defects. Correctness files are tiered by the judge's probability and location (pinned: p at least 0.80, placed with at least 0.60); both bounds are first picks, not yet measured. Maintainability ordering by severity and category breadth is an unvalidated candidate. Legacy scores are descriptive, not safety verdicts. Historical calibration is not current-model accuracy."

	reviewNextCheck = "Inspect the cited source with callers, ownership contracts and tests; confirm the consequence or reject the allegation."

	reviewSelection     = "%s candidate: maximum %s catalogue severity; %d distinct flagged categories; assessment %s. Category breadth is not a defect count."
	reviewSelectionTier = "correctness candidate, tier %s: strongest qualifying finding p=%.2f; %d distinct qualifying categories; assessment %s. Evidence strength is not a confirmed defect."

	intentUnread = "The intent answers could not be read (%s). No finding is hidden or lowered for it: every finding shows its intent as unknown and is ranked as not stated."

	fileChanged = "changed after it was planned; the next step plans it again"

	intentStopped = "The intent answers stopped being read (%s). Files read before that show their answers; files after it show intent none, unreadable. No flag is hidden or lowered for it."

	scoreCaveat = "The legacy index and bands estimate maintainability, not correctness. Measured once against people, on 2026-09-22 with the hosted judge and the catalogue of that day, not the one named here: 287 Java classes rated by 70 professionals (ICSME 2020 dataset), Spearman -0.66, AUC 0.95 between maintainable and not; with a 4-bit 4B local judge it was at chance; never measured on Go. Not measured at all: the correctness tiers and their 0.80 and 0.60 bounds, the intent label, the slop and outline questions, and every slop threshold. Worst entries are chunks, not distinct files; inspect assessment and coverage separately."

	staleCursor = "review cursor belongs to different scan evidence or query; restart at the first page"

	reviewEntryTooLarge = "one file review exceeds page_bytes; use a larger page or findings shape for its individual chunk evidence"

	pageBoundsInvalid = "page_bytes must be 4096..200000 and limit 1..100"
	tierInvalid       = "tier applies to review_queue and triage of the correctness lane, and is one of pinned, unpinned, intent_stated, all"

	rulesRetry = " — rules must validate before retry"

	continuesNextStep = "this file needs more judge calls than the step had left; the next step continues it with its full calls"
)
