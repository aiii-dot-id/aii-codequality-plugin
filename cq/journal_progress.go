package cq

type JournalProgress struct {
	Journal  *Journal
	Offset   int64
	end      int64
	verified bool
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
	return DeduplicateRecords(records), nil
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
	end, err := p.Journal.appendAt(p.end, e)
	if err != nil {
		p.verified = false
		return err
	}
	p.end = end
	return nil
}
func (p *JournalProgress) End() int64 { return p.end }
