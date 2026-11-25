package web

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// botsUsing returns the names of the bots that use an integration; column is
// "ai_integration_id" or "git_integration_id".
func botsUsing(db *sql.DB, column string, id int64) ([]string, error) {
	if column != "ai_integration_id" && column != "git_integration_id" {
		return nil, fmt.Errorf("unknown integration column %q", column)
	}
	rows, err := db.Query(`SELECT name FROM bots WHERE `+column+` = ? ORDER BY name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// inUseNotice explains why the integration in the "in_use" query parameter
// was not deleted. Only the id travels in the URL; the names come from the
// database, so a crafted link cannot put text of its choosing on the page.
func inUseNotice(db *sql.DB, table, column, inUseParam string) string {
	id, err := strconv.ParseInt(inUseParam, 10, 64)
	if err != nil || id <= 0 {
		return ""
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM `+table+` WHERE id = ?`, id).Scan(&name); err != nil {
		return ""
	}
	bots, err := botsUsing(db, column, id)
	if err != nil || len(bots) == 0 {
		return ""
	}
	noun, pronoun := "bot", "it"
	if len(bots) > 1 {
		noun, pronoun = "bots", "them"
	}
	return fmt.Sprintf("%q was not deleted: it is still used by %s %s. Switch %s to another integration first.",
		name, noun, quoteList(bots), pronoun)
}

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = strconv.Quote(n)
	}
	return strings.Join(q, ", ")
}
