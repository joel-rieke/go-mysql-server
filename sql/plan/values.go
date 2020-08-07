package plan

import (
	"fmt"

	"github.com/liquidata-inc/go-mysql-server/sql"
)

// Values represents a set of tuples of expressions.
type Values struct {
	ExpressionTuples [][]sql.Expression
}

// NewValues creates a Values node with the given tuples.
func NewValues(tuples [][]sql.Expression) *Values {
	return &Values{tuples}
}

// Schema implements the Node interface.
func (p *Values) Schema() sql.Schema {
	if len(p.ExpressionTuples) == 0 {
		return nil
	}

	exprs := p.ExpressionTuples[0]
	s := make(sql.Schema, len(exprs))
	for i, e := range exprs {
		var name string
		if n, ok := e.(sql.Nameable); ok {
			name = n.Name()
		} else {
			name = e.String()
		}
		s[i] = &sql.Column{
			Name:     name,
			Type:     e.Type(),
			Nullable: e.IsNullable(),
		}
	}

	return nil
}

// Children implements the Node interface.
func (p *Values) Children() []sql.Node {
	return nil
}

// Resolved implements the Resolvable interface.
func (p *Values) Resolved() bool {
	for _, et := range p.ExpressionTuples {
		if !expressionsResolved(et...) {
			return false
		}
	}

	return true
}

// RowIter implements the Node interface.
func (p *Values) RowIter(ctx *sql.Context, row sql.Row) (sql.RowIter, error) {
	rows := make([]sql.Row, len(p.ExpressionTuples))
	for i, et := range p.ExpressionTuples {
		vals := make([]interface{}, len(et))
		for j, e := range et {
			var err error
			vals[j], err = e.Eval(ctx, nil)
			if err != nil {
				return nil, err
			}
		}

		rows[i] = sql.NewRow(vals...)
	}

	return sql.RowsToRowIter(rows...), nil
}

func (p *Values) String() string {
	return fmt.Sprintf("Values(%d tuples)", len(p.ExpressionTuples))
}

// Expressions implements the Expressioner interface.
func (p *Values) Expressions() []sql.Expression {
	var exprs []sql.Expression
	for _, tuple := range p.ExpressionTuples {
		exprs = append(exprs, tuple...)
	}
	return exprs
}

// WithChildren implements the Node interface.
func (p *Values) WithChildren(children ...sql.Node) (sql.Node, error) {
	if len(children) != 0 {
		return nil, sql.ErrInvalidChildrenNumber.New(p, len(children), 0)
	}

	return p, nil
}

// WithExpressions implements the Expressioner interface.
func (p *Values) WithExpressions(exprs ...sql.Expression) (sql.Node, error) {
	var expected int
	for _, t := range p.ExpressionTuples {
		expected += len(t)
	}

	if len(exprs) != expected {
		return nil, sql.ErrInvalidChildrenNumber.New(p, len(exprs), expected)
	}

	var offset int
	var tuples = make([][]sql.Expression, len(p.ExpressionTuples))
	for i, t := range p.ExpressionTuples {
		for range t {
			tuples[i] = append(tuples[i], exprs[offset])
			offset++
		}
	}

	return NewValues(tuples), nil
}
