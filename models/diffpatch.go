package models

import (
	"fmt"
	"github.com/coopernurse/gorp"
	"time"
)

type DiffPatch struct {
	DiffId                                 int
	Name, Url, Artist, Title, DateAddedStr string

	// Is transient
	DateAdded    time.Time
	RequestCount int
}

const (
	DATE_FORMAT     = "Jan _2, 2006"
	SQL_DATE_FORMAT = "2006-01-02"
)

// These hooks work around two things:
// - Gorp's lack of support for loading relations automatically.
// - Sqlite's lack of support for datetimes.

func (p *DiffPatch) PreInsert(_ gorp.SqlExecutor) error {
	p.DateAddedStr = time.Now().Format(SQL_DATE_FORMAT)
	return nil
}

func (p *DiffPatch) PostGet(exe gorp.SqlExecutor) error {
	var (
		err error
	)

	if p.DateAdded, err = time.Parse(SQL_DATE_FORMAT, p.DateAddedStr); err != nil {
		return fmt.Errorf("Error parsing check in date '%s':", p.DateAddedStr, err)
	}
	return nil
}
