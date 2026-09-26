package recognize

import "docflow/internal/domain"

func freeTableForTest(cols []string, rows [][]string) *domain.FreeTable {
	return &domain.FreeTable{Columns: cols, Roles: rolesFor(cols), Rows: rows, Source: "columns"}
}
