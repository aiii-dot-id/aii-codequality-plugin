package cq

import "errors"

type JournalProgress struct {
	Journal      *Journal
	Intent       *Journal
	Offset       int64
	IntentOffset int64
	Room         int64
	end          int64
	verified     bool
	intentEnd    int64
	intentOK     bool
}

func (p *JournalProgress) Load(ref string) ([]Record, error) {
	var records []Record
	end, err := p.Journal.Walk(p.Offset, func(e JournalEvent) error {
		if e.Record != nil && e.Record.Ref == ref {
			records = append(records, *e.Record)
		}
		return nil
	})
	if err != nil {
		p.verified = false
		return nil, err
	}
	p.end, p.verified = end, true
	if p.Intent != nil {
		end, err := p.Intent.Walk(p.IntentOffset, func(e JournalEvent) error {
			if e.Record != nil && e.Record.Ref == ref && (e.Record.Battery == IntentBattery || e.Record.Battery == SlopBattery || e.Record.Battery == OutlineBattery) {
				records = append(records, *e.Record)
			}
			return nil
		})
		if err != nil {
			p.intentOK = false
			return nil, err
		}
		p.intentEnd, p.intentOK = end, true
	}
	return DeduplicateRecords(records), nil
}

func (p *JournalProgress) IntentEnd() int64 {
	if p.intentOK {
		return p.intentEnd
	}
	return p.IntentOffset
}

func (p *JournalProgress) Verify() error {
	end, err := p.Journal.Walk(p.Offset, nil)
	if err != nil {
		p.verified = false
		return err
	}
	p.end, p.verified = end, true
	return nil
}

func (p *JournalProgress) Save(r Record) error {
	if sideRecord(r) {
		if p.Intent == nil {
			return errNoIntentFile
		}
		if err := p.verifyIntent(); err != nil {
			return err
		}
		most := int64(0)
		if p.Room > 0 {
			if !p.verified {
				if err := p.Verify(); err != nil {
					return err
				}
			}
			most = max(p.Room-p.end, 1)
		}
		end, err := p.Intent.appendAt(p.intentEnd, most, JournalEvent{Record: &r})
		if err != nil {
			p.intentOK = false
			if errors.Is(err, ErrJournalFull) {
				return ErrIntentFull
			}
			return err
		}
		p.intentEnd = end
		return nil
	}
	return p.append(JournalEvent{Record: &r})
}
func (p *JournalProgress) Cover(c Coverage) error {
	return p.append(JournalEvent{Coverage: &c})
}
func (p *JournalProgress) append(e JournalEvent) error {
	if !p.verified {
		if err := p.Verify(); err != nil {
			return err
		}
	}
	most := int64(0)
	if p.Room > 0 && p.Intent != nil {
		if err := p.verifyIntent(); err != nil {
			return err
		}
		most = max(p.Room-p.intentEnd, 1)
	}
	end, err := p.Journal.appendAt(p.end, most, e)
	if err != nil {
		p.verified = false
		return err
	}
	p.end = end
	return nil
}
func (p *JournalProgress) End() int64 { return p.end }

func (p *JournalProgress) Left() (int64, error) {
	if err := p.Verify(); err != nil {
		return 0, err
	}
	limit, err := p.Journal.limit()
	if err != nil {
		return 0, err
	}
	left := limit - p.end
	if p.Room > 0 && p.Intent != nil {
		if err := p.verifyIntent(); err != nil {
			return 0, err
		}
		left = min(left, p.Room-p.end-p.intentEnd)
	}
	return left, nil
}

func (p *JournalProgress) HasRoom() (bool, error) {
	left, err := p.Left()
	return left >= journalEventBytes, err
}

func (p *JournalProgress) verifyIntent() error {
	if p.intentOK {
		return nil
	}
	end, err := p.Intent.Walk(p.IntentOffset, nil)
	if err != nil {
		return err
	}
	p.intentEnd, p.intentOK = end, true
	return nil
}
