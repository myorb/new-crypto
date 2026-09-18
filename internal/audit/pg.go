package audit

import "github.com/jackc/pgx/v5/pgtype"

func toInt2(v int) pgtype.Int2 {
	return pgtype.Int2{Int16: int16(v), Valid: true}
}
