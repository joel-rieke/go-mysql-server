// Copyright 2020-2021 Dolthub, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package enginetest

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dolthub/vitess/go/mysql"
	"github.com/dolthub/vitess/go/sqltypes"
	_ "github.com/go-sql-driver/mysql"
	"github.com/gocraft/dbr/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/src-d/go-errors.v1"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/enginetest/queries"
	"github.com/dolthub/go-mysql-server/enginetest/scriptgen/setup"
	"github.com/dolthub/go-mysql-server/server"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/analyzer"
	"github.com/dolthub/go-mysql-server/sql/expression"
	"github.com/dolthub/go-mysql-server/sql/expression/function/aggregation"
	"github.com/dolthub/go-mysql-server/sql/information_schema"
	"github.com/dolthub/go-mysql-server/sql/mysql_db"
	"github.com/dolthub/go-mysql-server/sql/mysql_db/serial"
	"github.com/dolthub/go-mysql-server/sql/parse"
	"github.com/dolthub/go-mysql-server/sql/plan"
	"github.com/dolthub/go-mysql-server/sql/transform"
	"github.com/dolthub/go-mysql-server/test"
)

// TestQueries tests a variety of queries against databases and tables provided by the given harness.
func TestQueries(t *testing.T, harness Harness) {
	harness.Setup(setup.SimpleSetup...)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)
	for _, tt := range queries.QueryTests {
		t.Run(tt.Query, func(t *testing.T) {
			if sh, ok := harness.(SkippingHarness); ok {
				if sh.SkipQueryTest(tt.Query) {
					t.Skipf("Skipping query plan for %s", tt.Query)
				}
			}
			TestQueryWithContext(t, ctx, e, tt.Query, tt.Expected, tt.ExpectedColumns, nil)
		})
	}

	if keyless, ok := harness.(KeylessTableHarness); ok && keyless.SupportsKeylessTables() {
		for _, tt := range queries.KeylessQueries {
			TestQuery(t, harness, tt.Query, tt.Expected, tt.ExpectedColumns, nil)
		}
	}
}

// Tests a variety of geometry queries against databases and tables provided by the given harness.
func TestSpatialQueries(t *testing.T, harness Harness) {
	harness.Setup(setup.SpatialSetup...)
	e := mustNewEngine(t, harness)
	defer e.Close()
	for _, tt := range queries.SpatialQueryTests {
		TestQueryWithEngine(t, harness, e, tt)
	}
}

// Tests a variety of geometry queries against databases and tables provided by the given harness.
func TestSpatialQueriesPrepared(t *testing.T, harness Harness) {
	harness.Setup(setup.SpatialSetup...)
	e := mustNewEngine(t, harness)
	defer e.Close()
	for _, tt := range queries.SpatialQueryTests {
		TestPreparedQueryWithEngine(t, harness, e, tt)
	}

	for _, tt := range queries.SpatialDeleteTests {
		runWriteQueryTestPrepared(t, harness, tt)
	}
	for _, tt := range queries.SpatialInsertQueries {
		runWriteQueryTestPrepared(t, harness, tt)
	}
	for _, tt := range queries.SpatialUpdateTests {
		runWriteQueryTestPrepared(t, harness, tt)
	}
}

// Tests join queries against a provided harness.
func TestJoinQueries(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData, setup.Pk_tablesData, setup.OthertableData)
	for _, tt := range queries.JoinQueryTests {
		TestQuery(t, harness, tt.Query, tt.Expected, tt.ExpectedColumns, nil)
	}

	t.Skip()
	for _, tt := range queries.SkippedJoinQueryTests {
		TestQuery(t, harness, tt.Query, tt.Expected, tt.ExpectedColumns, nil)
	}
}

// TestInfoSchemaPrepared runs tests of the information_schema database
func TestInfoSchemaPrepared(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData, setup.Fk_tblData, setup.FooData)
	for _, tt := range queries.InfoSchemaQueries {
		TestPreparedQuery(t, harness, tt.Query, tt.Expected, tt.ExpectedColumns)
	}
	harness.Setup(setup.MydbData, setup.MytableData, setup.Fk_tblData, setup.FooData)
	for _, script := range queries.InfoSchemaScripts {
		TestScriptPrepared(t, harness, script)
	}
}

func TestQueriesPrepared(t *testing.T, harness Harness) {
	harness.Setup(setup.SimpleSetup...)
	e := mustNewEngine(t, harness)
	defer e.Close()
	for _, tt := range queries.QueryTests {
		if tt.SkipPrepared {
			continue
		}
		TestPreparedQueryWithEngine(t, harness, e, tt)
	}

	harness.Setup(setup.MydbData, setup.KeylessData, setup.MytableData)
	for _, tt := range queries.KeylessQueries {
		TestPreparedQueryWithEngine(t, harness, e, tt)
	}

	harness.Setup(setup.MydbData)
	for _, tt := range queries.DateParseQueries {
		TestPreparedQueryWithEngine(t, harness, e, tt)
	}
}

func TestBrokenQueries(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData, setup.Pk_tablesData, setup.Fk_tblData)
	RunQueryTests(t, harness, queries.BrokenQueries)
}

func TestPreparedStaticIndexQuery(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	engine := mustNewEngine(t, harness)
	defer engine.Close()
	ctx := NewContext(harness)
	RunQueryWithContext(t, engine, ctx, "CREATE TABLE squares (i bigint primary key, square bigint);")
	engine.PrepareQuery(ctx, "select * from squares where i = 1")
	RunQueryWithContext(t, engine, ctx, "INSERT INTO squares VALUES (0, 0), (1, 1), (2, 4), (3, 9);")
	TestQueryWithContext(t, ctx, engine, "select * from squares where i = 1",
		[]sql.Row{{1, 1}}, sql.Schema{{Name: "i", Type: sql.Int64}, {Name: "square", Type: sql.Int64}}, nil)
}

// Runs the query tests given after setting up the engine. Useful for testing out a smaller subset of queries during
// debugging.
func RunQueryTests(t *testing.T, harness Harness, queries []queries.QueryTest) {
	for _, tt := range queries {
		TestQuery(t, harness, tt.Query, tt.Expected, tt.ExpectedColumns, nil)
	}
}

// TestInfoSchema runs tests of the information_schema database
func TestInfoSchema(t *testing.T, h Harness) {
	h.Setup(setup.MydbData, setup.MytableData, setup.Fk_tblData, setup.FooData)
	RunQueryTests(t, h, queries.InfoSchemaQueries)

	for _, script := range queries.InfoSchemaScripts {
		TestScript(t, h, script)
	}

	t.Run("information_schema.processlist", func(t *testing.T) {
		e := mustNewEngine(t, h)
		defer e.Close()
		p := sqle.NewProcessList()
		sess := sql.NewBaseSessionWithClientServer("localhost", sql.Client{Address: "localhost", User: "root"}, 1)
		ctx := sql.NewContext(context.Background(), sql.WithPid(1), sql.WithSession(sess), sql.WithProcessList(p))

		ctx, err := p.AddProcess(ctx, "SELECT foo")
		require.NoError(t, err)

		TestQueryWithContext(t, ctx, e, "SELECT * FROM information_schema.processlist", []sql.Row{{1, "root", "localhost", "NULL", "Query", 0, "processlist(processlist (0/? partitions))", "SELECT foo"}}, nil, nil)
		require.NoError(t, err)
	})
}

func CreateIndexes(t *testing.T, harness Harness, engine *sqle.Engine) {
	if ih, ok := harness.(IndexHarness); ok && ih.SupportsNativeIndexCreation() {
		err := createNativeIndexes(t, harness, engine)
		require.NoError(t, err)
	}
}

func createForeignKeys(t *testing.T, harness Harness, engine *sqle.Engine) {
	if fkh, ok := harness.(ForeignKeyHarness); ok && fkh.SupportsForeignKeys() {
		ctx := NewContextWithEngine(harness, engine)
		TestQueryWithContext(t, ctx, engine, "ALTER TABLE mytable ADD INDEX idx_si (s,i)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		TestQueryWithContext(t, ctx, engine, "ALTER TABLE fk_tbl ADD CONSTRAINT fk1 FOREIGN KEY (a,b) REFERENCES mytable (i,s) ON DELETE CASCADE", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
	}
}

func createReadOnlyDatabases(h ReadOnlyDatabaseHarness) (dbs []sql.Database) {
	for _, r := range h.NewReadOnlyDatabases("mydb", "foo") {
		dbs = append(dbs, sql.Database(r)) // FURP
	}
	return dbs
}

func TestReadOnlyDatabases(t *testing.T, harness Harness) {
	ro, ok := harness.(ReadOnlyDatabaseHarness)
	if !ok {
		t.Fatal("harness is not ReadOnlyDatabaseHarness")
	}
	dbs := createReadOnlyDatabases(ro)
	dbs = createSubsetTestData(t, harness, nil, dbs[0], dbs[1])
	engine := NewEngineWithDbs(t, harness, dbs)
	defer engine.Close()

	for _, querySet := range [][]queries.QueryTest{
		queries.QueryTests,
		queries.KeylessQueries,
		queries.VersionedQueries,
	} {
		for _, tt := range querySet {
			TestQueryWithEngine(t, harness, engine, tt)
		}
	}

	for _, querySet := range [][]queries.WriteQueryTest{
		queries.InsertQueries,
		queries.UpdateTests,
		queries.DeleteTests,
		queries.ReplaceQueries,
	} {
		for _, tt := range querySet {
			t.Run(tt.WriteQuery, func(t *testing.T) {
				AssertErrWithBindings(t, engine, harness, tt.WriteQuery, tt.Bindings, analyzer.ErrReadOnlyDatabase)
			})
		}
	}
}

// Tests generating the correct query plans for various queries using databases and tables provided by the given
// harness.
func TestQueryPlans(t *testing.T, harness Harness) {
	harness.Setup(setup.SimpleSetup...)
	e := mustNewEngine(t, harness)
	defer e.Close()
	for _, tt := range queries.PlanTests {
		TestQueryPlan(t, harness, e, tt.Query, tt.ExpectedPlan)
	}
}

func TestIndexQueryPlans(t *testing.T, harness Harness) {
	harness.Setup(setup.ComplexIndexSetup...)
	e := mustNewEngine(t, harness)
	defer e.Close()
	for _, tt := range queries.IndexPlanTests {
		TestQueryPlanWithEngine(t, harness, e, tt)
	}

	t.Run("no database selected", func(t *testing.T) {
		ctx := NewContext(harness)
		ctx.SetCurrentDatabase("")

		RunQuery(t, e, harness, "CREATE DATABASE otherdb")
		RunQuery(t, e, harness, `CREATE TABLE otherdb.a (x int, y int)`)
		RunQuery(t, e, harness, `CREATE INDEX idx1 ON otherdb.a (y);`)

		TestQueryWithContext(t, ctx, e, "SHOW INDEXES FROM otherdb.a", []sql.Row{
			{"a", 1, "idx1", 1, "y", nil, 0, nil, nil, "YES", "BTREE", "", "", "YES", nil},
		}, nil, nil)

	})
}

// Tests a variety of queries against databases and tables provided by the given harness.
func TestVersionedQueries(t *testing.T, harness Harness) {
	if _, ok := harness.(VersionedDBHarness); !ok {
		t.Skipf("Skipping versioned test, harness doesn't implement VersionedDBHarness")
	}

	harness.Setup(setup.SimpleSetup...)
	engine := NewEngine(t, harness)
	defer engine.Close()

	for _, tt := range queries.VersionedQueries {
		TestQueryWithEngine(t, harness, engine, tt)
	}

	for _, tt := range queries.VersionedScripts {
		TestScriptWithEngine(t, engine, harness, tt)
	}

	// These queries return different errors in the Memory engine and in the Dolt engine.
	// Memory engine returns ErrTableNotFound, while Dolt engine returns ErrBranchNotFound.
	// Until that is fixed, this test will not pass in both GMS and Dolt.
	skippedTests := []queries.ScriptTest{
		{
			Query:       "DESCRIBE myhistorytable AS OF '2018-12-01'",
			ExpectedErr: sql.ErrTableNotFound,
		},
		{
			Query:       "SHOW CREATE TABLE myhistorytable AS OF '2018-12-01'",
			ExpectedErr: sql.ErrTableNotFound,
		},
	}
	for _, skippedTest := range skippedTests {
		t.Run(skippedTest.Query, func(t *testing.T) {
			t.Skip()
			TestScript(t, harness, skippedTest)
		})
	}
}

// Tests a variety of queries against databases and tables provided by the given harness.
func TestVersionedQueriesPrepared(t *testing.T, harness Harness) {
	if _, ok := harness.(VersionedDBHarness); !ok {
		t.Skipf("Skipping versioned test, harness doesn't implement VersionedDBHarness")
	}

	e := NewEngine(t, harness)
	defer e.Close()

	for _, tt := range queries.VersionedQueries {
		TestPreparedQueryWithEngine(t, harness, e, tt)
	}

	t.Skip("skipping tests that version using UserVars instead of BindVars")
	for _, tt := range queries.VersionedScripts {
		TestScriptPrepared(t, harness, tt)
	}
}

// TestQueryPlan analyzes the query given and asserts that its printed plan matches the expected one.
func TestQueryPlan(t *testing.T, harness Harness, e *sqle.Engine, query string, expectedPlan string) {
	t.Run(query, func(t *testing.T) {
		ctx := NewContext(harness)
		parsed, err := parse.Parse(ctx, query)
		require.NoError(t, err)

		node, err := e.Analyzer.Analyze(ctx, parsed, nil)
		require.NoError(t, err)

		if sh, ok := harness.(SkippingHarness); ok {
			if sh.SkipQueryTest(query) {
				t.Skipf("Skipping query plan for %s", query)
			}
		}

		assert.Equal(t, expectedPlan, extractQueryNode(node).String(), "Unexpected result for query: "+query)
	})

}

func TestQueryPlanWithEngine(t *testing.T, harness Harness, e *sqle.Engine, tt queries.QueryPlanTest) {
	t.Run(tt.Query, func(t *testing.T) {
		ctx := NewContext(harness)
		parsed, err := parse.Parse(ctx, tt.Query)
		require.NoError(t, err)

		node, err := e.Analyzer.Analyze(ctx, parsed, nil)
		require.NoError(t, err)

		if sh, ok := harness.(SkippingHarness); ok {
			if sh.SkipQueryTest(tt.Query) {
				t.Skipf("Skipping query plan for %s", tt.Query)
			}
		}

		assert.Equal(t, tt.ExpectedPlan, extractQueryNode(node).String(), "Unexpected result for query: "+tt.Query)
	})

}

func extractQueryNode(node sql.Node) sql.Node {
	switch node := node.(type) {
	case *plan.QueryProcess:
		return extractQueryNode(node.Child())
	case *analyzer.Releaser:
		return extractQueryNode(node.Child)
	default:
		return node
	}
}

func TestOrderByGroupBy(t *testing.T, harness Harness) {
	require := require.New(t)

	harness.Setup([]setup.SetupScript{{
		"create database mydb",
		"use mydb",
		"create table members (id bigint primary key, team text)",
		"insert into members values (3,'red'), (4,'red'),(5,'orange'),(6,'orange'),(7,'orange'),(8,'purple')",
	}})
	e := mustNewEngine(t, harness)
	defer e.Close()

	sch, iter, err := e.Query(NewContext(harness), "SELECT team, COUNT(*) FROM members GROUP BY team ORDER BY 2")
	require.NoError(err)

	ctx := NewContext(harness)
	rows, err := sql.RowIterToRows(ctx, sch, iter)
	require.NoError(err)

	expected := []sql.Row{
		{"purple", int64(1)},
		{"red", int64(2)},
		{"orange", int64(3)},
	}

	require.Equal(expected, rows)

	sch, iter, err = e.Query(NewContext(harness), "SELECT team, COUNT(*) FROM members GROUP BY 1 ORDER BY 2")
	require.NoError(err)

	rows, err = sql.RowIterToRows(ctx, sch, iter)
	require.NoError(err)

	require.Equal(expected, rows)

	_, _, err = e.Query(NewContext(harness), "SELECT team, COUNT(*) FROM members GROUP BY team ORDER BY columndoesnotexist")
	require.Error(err)
}

func TestReadOnly(t *testing.T, harness Harness) {
	harness.Setup(setup.Mytable...)
	e := mustNewEngine(t, harness)
	e.IsReadOnly = true
	defer e.Close()

	RunQuery(t, e, harness, `SELECT i FROM mytable`)

	writingQueries := []string{
		`CREATE INDEX foo USING BTREE ON mytable (i, s)`,
		`DROP INDEX foo ON mytable`,
		`INSERT INTO mytable (i, s) VALUES(42, 'yolo')`,
		`CREATE VIEW myview3 AS SELECT i FROM mytable`,
		`DROP VIEW myview`,
	}

	for _, query := range writingQueries {
		AssertErr(t, e, harness, query, sql.ErrNotAuthorized)
	}
}

// TestColumnAliases exercises the logic for naming and referring to column aliases, and unlike other tests in this
// file checks that the name of the columns in the result schema is correct.
func TestColumnAliases(t *testing.T, harness Harness) {
	harness.Setup(setup.Mytable...)
	for _, tt := range queries.ColumnAliasQueries {
		TestQuery(t, harness, tt.Query, tt.Expected, tt.ExpectedColumns, nil)
	}
}

func TestAmbiguousColumnResolution(t *testing.T, harness Harness) {
	harness.Setup([]setup.SetupScript{{
		"create database mydb",
		"use mydb",
		"create table foo (a bigint primary key, b text)",
		"create table bar (b text primary key, c bigint)",
		"insert into foo values (1, 'foo'), (2,'bar'), (3,'baz')",
		"insert into bar values ('qux',3), ('mux',2), ('pux',1)",
	}})
	e := mustNewEngine(t, harness)
	defer e.Close()

	ctx := NewContext(harness)
	expected := []sql.Row{
		{int64(1), "pux", "foo"},
		{int64(2), "mux", "bar"},
		{int64(3), "qux", "baz"},
	}
	TestQueryWithContext(t, ctx, e, `SELECT f.a, bar.b, f.b FROM foo f INNER JOIN bar ON f.a = bar.c order by 1`, expected, nil, nil)
}

func TestQueryErrors(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData, setup.Pk_tablesData, setup.MyhistorytableData)
	for _, tt := range queries.ErrorQueries {
		runQueryErrorTest(t, harness, tt)
	}
}

func MustQuery(ctx *sql.Context, e *sqle.Engine, q string) []sql.Row {
	sch, iter, err := e.Query(ctx, q)
	if err != nil {
		panic(err)
	}
	rows, err := sql.RowIterToRows(ctx, sch, iter)
	if err != nil {
		panic(err)
	}
	return rows
}

func TestInsertInto(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData, setup.Mytable_del_idxData, setup.KeylessData, setup.NiltableData, setup.TypestableData, setup.EmptytableData, setup.AutoincrementData, setup.OthertableData, setup.Othertable_del_idxData)
	for _, insertion := range queries.InsertQueries {
		runWriteQueryTest(t, harness, insertion)
	}

	harness.Setup(setup.MydbData)
	for _, script := range queries.InsertScripts {
		TestScript(t, harness, script)
	}
}

func TestInsertIgnoreInto(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	for _, script := range queries.InsertIgnoreScripts {
		TestScript(t, harness, script)
	}
}

func TestInsertIntoErrors(t *testing.T, harness Harness) {
	harness.Setup(setup.Mytable...)
	for _, expectedFailure := range queries.InsertErrorTests {
		runGenericErrorTest(t, harness, expectedFailure)
	}

	harness.Setup(setup.MydbData)
	for _, script := range queries.InsertErrorScripts {
		TestScript(t, harness, script)
	}
}

func TestBrokenInsertScripts(t *testing.T, harness Harness) {
	for _, script := range queries.InsertScripts {
		t.Skip()
		TestScript(t, harness, script)
	}
}

func TestSpatialInsertInto(t *testing.T, harness Harness) {
	harness.Setup(setup.SpatialSetup...)
	for _, tt := range queries.SpatialInsertQueries {
		runWriteQueryTest(t, harness, tt)
	}
}

func TestLoadData(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	for _, script := range queries.LoadDataScripts {
		TestScript(t, harness, script)
	}
}

func TestLoadDataErrors(t *testing.T, harness Harness) {
	for _, script := range queries.LoadDataErrorScripts {
		TestScript(t, harness, script)
	}
}

func TestLoadDataFailing(t *testing.T, harness Harness) {
	t.Skip()
	for _, script := range queries.LoadDataFailingScripts {
		TestScript(t, harness, script)
	}
}

func TestReplaceInto(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData, setup.Mytable_del_idxData, setup.TypestableData)
	for _, tt := range queries.ReplaceQueries {
		runWriteQueryTest(t, harness, tt)
	}
}

func TestReplaceIntoErrors(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData)
	for _, tt := range queries.ReplaceErrorTests {
		runGenericErrorTest(t, harness, tt)
	}
}

func TestUpdate(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData, setup.Mytable_del_idxData, setup.FloattableData, setup.NiltableData, setup.TypestableData, setup.Pk_tablesData, setup.OthertableData, setup.TabletestData)
	for _, tt := range queries.UpdateTests {
		runWriteQueryTest(t, harness, tt)
	}
}

func TestUpdateErrors(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData, setup.FloattableData, setup.TypestableData)
	for _, expectedFailure := range queries.GenericUpdateErrorTests {
		runGenericErrorTest(t, harness, expectedFailure)
	}

	harness.Setup(setup.MydbData, setup.KeylessData, setup.PeopleData)
	for _, expectedFailure := range queries.UpdateErrorTests {
		runQueryErrorTest(t, harness, expectedFailure)
	}

	for _, script := range queries.UpdateErrorScripts {
		TestScript(t, harness, script)
	}
}

func TestSpatialUpdate(t *testing.T, harness Harness) {
	harness.Setup(setup.SpatialSetup...)
	for _, update := range queries.SpatialUpdateTests {
		runWriteQueryTest(t, harness, update)
	}
}

func TestDelete(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData, setup.TabletestData)
	for _, tt := range queries.DeleteTests {
		runWriteQueryTest(t, harness, tt)
	}
}

func runWriteQueryTest(t *testing.T, harness Harness, tt queries.WriteQueryTest) {
	t.Run(tt.WriteQuery, func(t *testing.T) {
		if sh, ok := harness.(SkippingHarness); ok {
			if sh.SkipQueryTest(tt.WriteQuery) {
				t.Logf("Skipping query %s", tt.WriteQuery)
				return
			}
			if sh.SkipQueryTest(tt.SelectQuery) {
				t.Logf("Skipping query %s", tt.SelectQuery)
				return
			}
		}
		e := mustNewEngine(t, harness)
		ctx := NewContext(harness)
		defer e.Close()
		TestQueryWithContext(t, ctx, e, tt.WriteQuery, tt.ExpectedWriteResult, nil, nil)
		TestQueryWithContext(t, ctx, e, tt.SelectQuery, tt.ExpectedSelect, nil, nil)
	})
}

func runWriteQueryTestPrepared(t *testing.T, harness Harness, tt queries.WriteQueryTest) {
	t.Run(tt.WriteQuery, func(t *testing.T) {
		if sh, ok := harness.(SkippingHarness); ok {
			if sh.SkipQueryTest(tt.WriteQuery) {
				t.Logf("Skipping query %s", tt.WriteQuery)
				return
			}
			if sh.SkipQueryTest(tt.SelectQuery) {
				t.Logf("Skipping query %s", tt.SelectQuery)
				return
			}
		}
		e := mustNewEngine(t, harness)
		ctx := NewContext(harness)
		defer e.Close()
		TestPreparedQueryWithContext(t, ctx, e, tt.WriteQuery, tt.ExpectedWriteResult, nil)
		TestPreparedQueryWithContext(t, ctx, e, tt.SelectQuery, tt.ExpectedSelect, nil)
	})
}

func runGenericErrorTest(t *testing.T, h Harness, tt queries.GenericErrorQueryTest) {
	t.Run(tt.Name, func(t *testing.T) {
		if sh, ok := h.(SkippingHarness); ok {
			if sh.SkipQueryTest(tt.Query) {
				t.Skipf("skipping query %s", tt.Query)
			}
		}
		e := mustNewEngine(t, h)
		defer e.Close()
		AssertErr(t, e, h, tt.Query, nil)
	})
}

func runQueryErrorTest(t *testing.T, h Harness, tt queries.QueryErrorTest) {
	t.Run(tt.Query, func(t *testing.T) {
		if sh, ok := h.(SkippingHarness); ok {
			if sh.SkipQueryTest(tt.Query) {
				t.Skipf("skipping query %s", tt.Query)
			}
		}
		e := mustNewEngine(t, h)
		defer e.Close()
		AssertErr(t, e, h, tt.Query, nil)
	})
}

func TestUpdateQueriesPrepared(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData, setup.Mytable_del_idxData, setup.OthertableData, setup.TypestableData, setup.Pk_tablesData, setup.FloattableData, setup.NiltableData, setup.TabletestData)
	for _, tt := range queries.UpdateTests {
		runWriteQueryTestPrepared(t, harness, tt)
	}
}

func TestDeleteQueriesPrepared(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData, setup.TabletestData)
	for _, tt := range queries.DeleteTests {
		runWriteQueryTestPrepared(t, harness, tt)
	}
}

func TestInsertQueriesPrepared(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData, setup.Mytable_del_idxData, setup.KeylessData, setup.TypestableData, setup.NiltableData, setup.EmptytableData, setup.AutoincrementData, setup.OthertableData)
	for _, tt := range queries.InsertQueries {
		runWriteQueryTestPrepared(t, harness, tt)
	}
}

func TestReplaceQueriesPrepared(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData, setup.Mytable_del_idxData, setup.TypestableData)
	for _, tt := range queries.ReplaceQueries {
		runWriteQueryTestPrepared(t, harness, tt)
	}
}

func TestDeleteErrors(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData)
	for _, expectedFailure := range queries.DeleteErrorTests {
		runGenericErrorTest(t, harness, expectedFailure)
	}
}

func TestSpatialDelete(t *testing.T, harness Harness) {
	harness.Setup(setup.SpatialSetup...)
	for _, delete := range queries.SpatialDeleteTests {
		runWriteQueryTest(t, harness, delete)
	}
}

func TestTruncate(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	t.Run("Standard TRUNCATE", func(t *testing.T) {
		RunQuery(t, e, harness, "CREATE TABLE t1 (pk BIGINT PRIMARY KEY, v1 BIGINT, INDEX(v1))")
		RunQuery(t, e, harness, "INSERT INTO t1 VALUES (1,1), (2,2), (3,3)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t1 ORDER BY 1", []sql.Row{{int64(1), int64(1)}, {int64(2), int64(2)}, {int64(3), int64(3)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "TRUNCATE t1", []sql.Row{{sql.NewOkResult(3)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t1 ORDER BY 1", []sql.Row{}, nil, nil)

		RunQuery(t, e, harness, "INSERT INTO t1 VALUES (4,4), (5,5)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t1 WHERE v1 > 0 ORDER BY 1", []sql.Row{{int64(4), int64(4)}, {int64(5), int64(5)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "TRUNCATE TABLE t1", []sql.Row{{sql.NewOkResult(2)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t1 ORDER BY 1", []sql.Row{}, nil, nil)
	})

	t.Run("Foreign Key References", func(t *testing.T) {
		RunQuery(t, e, harness, "CREATE TABLE t2parent (pk BIGINT PRIMARY KEY, v1 BIGINT, INDEX (v1))")
		RunQuery(t, e, harness, "CREATE TABLE t2child (pk BIGINT PRIMARY KEY, v1 BIGINT, "+
			"FOREIGN KEY (v1) REFERENCES t2parent (v1))")
		_, _, err := e.Query(ctx, "TRUNCATE t2parent")
		require.True(t, sql.ErrTruncateReferencedFromForeignKey.Is(err))
	})

	t.Run("ON DELETE Triggers", func(t *testing.T) {
		RunQuery(t, e, harness, "CREATE TABLE t3 (pk BIGINT PRIMARY KEY, v1 BIGINT)")
		RunQuery(t, e, harness, "CREATE TABLE t3i (pk BIGINT PRIMARY KEY, v1 BIGINT)")
		RunQuery(t, e, harness, "CREATE TRIGGER trig_t3 BEFORE DELETE ON t3 FOR EACH ROW INSERT INTO t3i VALUES (old.pk, old.v1)")
		RunQuery(t, e, harness, "INSERT INTO t3 VALUES (1,1), (3,3)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t3 ORDER BY 1", []sql.Row{{int64(1), int64(1)}, {int64(3), int64(3)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "TRUNCATE t3", []sql.Row{{sql.NewOkResult(2)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t3 ORDER BY 1", []sql.Row{}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t3i ORDER BY 1", []sql.Row{}, nil, nil)
	})

	t.Run("auto_increment column", func(t *testing.T) {
		RunQuery(t, e, harness, "CREATE TABLE t4 (pk BIGINT AUTO_INCREMENT PRIMARY KEY, v1 BIGINT)")
		RunQuery(t, e, harness, "INSERT INTO t4(v1) VALUES (5), (6)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t4 ORDER BY 1", []sql.Row{{int64(1), int64(5)}, {int64(2), int64(6)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "TRUNCATE t4", []sql.Row{{sql.NewOkResult(2)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t4 ORDER BY 1", []sql.Row{}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t4(v1) VALUES (7)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t4 ORDER BY 1", []sql.Row{{int64(1), int64(7)}}, nil, nil)
	})

	t.Run("Naked DELETE", func(t *testing.T) {
		RunQuery(t, e, harness, "CREATE TABLE t5 (pk BIGINT PRIMARY KEY, v1 BIGINT)")
		RunQuery(t, e, harness, "INSERT INTO t5 VALUES (1,1), (2,2)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t5 ORDER BY 1", []sql.Row{{int64(1), int64(1)}, {int64(2), int64(2)}}, nil, nil)

		deleteStr := "DELETE FROM t5"
		parsed, err := parse.Parse(ctx, deleteStr)
		require.NoError(t, err)
		analyzed, err := e.Analyzer.Analyze(ctx, parsed, nil)
		require.NoError(t, err)
		truncateFound := false
		transform.Inspect(analyzed, func(n sql.Node) bool {
			switch n.(type) {
			case *plan.Truncate:
				truncateFound = true
				return false
			}
			return true
		})
		if !truncateFound {
			require.FailNow(t, "DELETE did not convert to TRUNCATE",
				"Expected Truncate Node, got:\n%s", analyzed.String())
		}

		TestQueryWithContext(t, ctx, e, deleteStr, []sql.Row{{sql.NewOkResult(2)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t5 ORDER BY 1", []sql.Row{}, nil, nil)
	})

	t.Run("Naked DELETE with Foreign Key References", func(t *testing.T) {
		RunQuery(t, e, harness, "CREATE TABLE t6parent (pk BIGINT PRIMARY KEY, v1 BIGINT, INDEX (v1))")
		RunQuery(t, e, harness, "CREATE TABLE t6child (pk BIGINT PRIMARY KEY, v1 BIGINT, "+
			"CONSTRAINT fk_a123 FOREIGN KEY (v1) REFERENCES t6parent (v1))")
		RunQuery(t, e, harness, "INSERT INTO t6parent VALUES (1,1), (2,2)")
		RunQuery(t, e, harness, "INSERT INTO t6child VALUES (1,1), (2,2)")

		parsed, err := parse.Parse(ctx, "DELETE FROM t6parent")
		require.NoError(t, err)
		analyzed, err := e.Analyzer.Analyze(ctx, parsed, nil)
		require.NoError(t, err)
		truncateFound := false
		transform.Inspect(analyzed, func(n sql.Node) bool {
			switch n.(type) {
			case *plan.Truncate:
				truncateFound = true
				return false
			}
			return true
		})
		if truncateFound {
			require.FailNow(t, "Incorrectly converted DELETE with fks to TRUNCATE")
		}
	})

	t.Run("Naked DELETE with ON DELETE Triggers", func(t *testing.T) {
		RunQuery(t, e, harness, "CREATE TABLE t7 (pk BIGINT PRIMARY KEY, v1 BIGINT)")
		RunQuery(t, e, harness, "CREATE TABLE t7i (pk BIGINT PRIMARY KEY, v1 BIGINT)")
		RunQuery(t, e, harness, "CREATE TRIGGER trig_t7 BEFORE DELETE ON t7 FOR EACH ROW INSERT INTO t7i VALUES (old.pk, old.v1)")
		RunQuery(t, e, harness, "INSERT INTO t7 VALUES (1,1), (3,3)")
		RunQuery(t, e, harness, "DELETE FROM t7 WHERE pk = 3")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t7 ORDER BY 1", []sql.Row{{int64(1), int64(1)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t7i ORDER BY 1", []sql.Row{{int64(3), int64(3)}}, nil, nil)

		deleteStr := "DELETE FROM t7"
		parsed, err := parse.Parse(ctx, deleteStr)
		require.NoError(t, err)
		analyzed, err := e.Analyzer.Analyze(ctx, parsed, nil)
		require.NoError(t, err)
		truncateFound := false
		transform.Inspect(analyzed, func(n sql.Node) bool {
			switch n.(type) {
			case *plan.Truncate:
				truncateFound = true
				return false
			}
			return true
		})
		if truncateFound {
			require.FailNow(t, "Incorrectly converted DELETE with triggers to TRUNCATE")
		}

		TestQueryWithContext(t, ctx, e, deleteStr, []sql.Row{{sql.NewOkResult(1)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t7 ORDER BY 1", []sql.Row{}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t7i ORDER BY 1", []sql.Row{{int64(1), int64(1)}, {int64(3), int64(3)}}, nil, nil)
	})

	t.Run("Naked DELETE with auto_increment column", func(t *testing.T) {
		RunQuery(t, e, harness, "CREATE TABLE t8 (pk BIGINT AUTO_INCREMENT PRIMARY KEY, v1 BIGINT)")
		RunQuery(t, e, harness, "INSERT INTO t8(v1) VALUES (4), (5)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t8 ORDER BY 1", []sql.Row{{int64(1), int64(4)}, {int64(2), int64(5)}}, nil, nil)

		deleteStr := "DELETE FROM t8"
		parsed, err := parse.Parse(ctx, deleteStr)
		require.NoError(t, err)
		analyzed, err := e.Analyzer.Analyze(ctx, parsed, nil)
		require.NoError(t, err)
		truncateFound := false
		transform.Inspect(analyzed, func(n sql.Node) bool {
			switch n.(type) {
			case *plan.Truncate:
				truncateFound = true
				return false
			}
			return true
		})
		if truncateFound {
			require.FailNow(t, "Incorrectly converted DELETE with auto_increment cols to TRUNCATE")
		}

		TestQueryWithContext(t, ctx, e, deleteStr, []sql.Row{{sql.NewOkResult(2)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t8 ORDER BY 1", []sql.Row{}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t8(v1) VALUES (6)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t8 ORDER BY 1", []sql.Row{{int64(3), int64(6)}}, nil, nil)
	})

	t.Run("DELETE with WHERE clause", func(t *testing.T) {
		RunQuery(t, e, harness, "CREATE TABLE t9 (pk BIGINT PRIMARY KEY, v1 BIGINT)")
		RunQuery(t, e, harness, "INSERT INTO t9 VALUES (7,7), (8,8)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t9 ORDER BY 1", []sql.Row{{int64(7), int64(7)}, {int64(8), int64(8)}}, nil, nil)

		deleteStr := "DELETE FROM t9 WHERE pk > 0"
		parsed, err := parse.Parse(ctx, deleteStr)
		require.NoError(t, err)
		analyzed, err := e.Analyzer.Analyze(ctx, parsed, nil)
		require.NoError(t, err)
		truncateFound := false
		transform.Inspect(analyzed, func(n sql.Node) bool {
			switch n.(type) {
			case *plan.Truncate:
				truncateFound = true
				return false
			}
			return true
		})
		if truncateFound {
			require.FailNow(t, "Incorrectly converted DELETE with WHERE clause to TRUNCATE")
		}

		TestQueryWithContext(t, ctx, e, deleteStr, []sql.Row{{sql.NewOkResult(2)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t9 ORDER BY 1", []sql.Row{}, nil, nil)
	})

	t.Run("DELETE with LIMIT clause", func(t *testing.T) {
		RunQuery(t, e, harness, "CREATE TABLE t10 (pk BIGINT PRIMARY KEY, v1 BIGINT)")
		RunQuery(t, e, harness, "INSERT INTO t10 VALUES (8,8), (9,9)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t10 ORDER BY 1", []sql.Row{{int64(8), int64(8)}, {int64(9), int64(9)}}, nil, nil)

		deleteStr := "DELETE FROM t10 LIMIT 1000"
		parsed, err := parse.Parse(ctx, deleteStr)
		require.NoError(t, err)
		analyzed, err := e.Analyzer.Analyze(ctx, parsed, nil)
		require.NoError(t, err)
		truncateFound := false
		transform.Inspect(analyzed, func(n sql.Node) bool {
			switch n.(type) {
			case *plan.Truncate:
				truncateFound = true
				return false
			}
			return true
		})
		if truncateFound {
			require.FailNow(t, "Incorrectly converted DELETE with LIMIT clause to TRUNCATE")
		}

		TestQueryWithContext(t, ctx, e, deleteStr, []sql.Row{{sql.NewOkResult(2)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t10 ORDER BY 1", []sql.Row{}, nil, nil)
	})

	t.Run("DELETE with ORDER BY clause", func(t *testing.T) {
		RunQuery(t, e, harness, "CREATE TABLE t11 (pk BIGINT PRIMARY KEY, v1 BIGINT)")
		RunQuery(t, e, harness, "INSERT INTO t11 VALUES (1,1), (9,9)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t11 ORDER BY 1", []sql.Row{{int64(1), int64(1)}, {int64(9), int64(9)}}, nil, nil)

		deleteStr := "DELETE FROM t11 ORDER BY 1"
		parsed, err := parse.Parse(ctx, deleteStr)
		require.NoError(t, err)
		analyzed, err := e.Analyzer.Analyze(ctx, parsed, nil)
		require.NoError(t, err)
		truncateFound := false
		transform.Inspect(analyzed, func(n sql.Node) bool {
			switch n.(type) {
			case *plan.Truncate:
				truncateFound = true
				return false
			}
			return true
		})
		if truncateFound {
			require.FailNow(t, "Incorrectly converted DELETE with ORDER BY clause to TRUNCATE")
		}

		TestQueryWithContext(t, ctx, e, deleteStr, []sql.Row{{sql.NewOkResult(2)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t11 ORDER BY 1", []sql.Row{}, nil, nil)
	})

	t.Run("Multi-table DELETE", func(t *testing.T) {
		t.Skip("Multi-table DELETE currently broken")
		RunQuery(t, e, harness, "CREATE TABLE t12a (pk BIGINT PRIMARY KEY, v1 BIGINT)")
		RunQuery(t, e, harness, "CREATE TABLE t12b (pk BIGINT PRIMARY KEY, v1 BIGINT)")
		RunQuery(t, e, harness, "INSERT INTO t12a VALUES (1,1), (2,2)")
		RunQuery(t, e, harness, "INSERT INTO t12b VALUES (1,1), (2,2)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t12a ORDER BY 1", []sql.Row{{int64(1), int64(1)}, {int64(2), int64(2)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t12b ORDER BY 1", []sql.Row{{int64(1), int64(1)}, {int64(2), int64(2)}}, nil, nil)

		deleteStr := "DELETE t12a, t12b FROM t12a INNER JOIN t12b WHERE t12a.pk=t12b.pk"
		parsed, err := parse.Parse(ctx, deleteStr)
		require.NoError(t, err)
		analyzed, err := e.Analyzer.Analyze(ctx, parsed, nil)
		require.NoError(t, err)
		truncateFound := false
		transform.Inspect(analyzed, func(n sql.Node) bool {
			switch n.(type) {
			case *plan.Truncate:
				truncateFound = true
				return false
			}
			return true
		})
		if truncateFound {
			require.FailNow(t, "Incorrectly converted DELETE with WHERE clause to TRUNCATE")
		}

		TestQueryWithContext(t, ctx, e, deleteStr, []sql.Row{{sql.NewOkResult(4)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t12a ORDER BY 1", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t12b ORDER BY 1", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
	})
}

func TestScripts(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	for _, script := range queries.ScriptTests {
		if sh, ok := harness.(SkippingHarness); ok {
			if sh.SkipQueryTest(script.Name) {
				t.Run(script.Name, func(t *testing.T) {
					t.Skip(script.Name)
				})
				continue
			}
		}
		TestScript(t, harness, script)
	}
}

func TestSpatialScripts(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	for _, script := range queries.SpatialScriptTests {

		TestScript(t, harness, script)
	}
}

func TestLoadDataPrepared(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	for _, script := range queries.LoadDataScripts {
		TestScriptPrepared(t, harness, script)
	}
}

func TestScriptsPrepared(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	for _, script := range append(queries.ScriptTests, queries.SpatialScriptTests...) {
		if sh, ok := harness.(SkippingHarness); ok {
			if sh.SkipQueryTest(script.Name) {
				t.Run(script.Name, func(t *testing.T) {
					t.Skip(script.Name)
				})
				continue
			}
		}
		TestScriptPrepared(t, harness, script)
	}
}

func TestInsertScriptsPrepared(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	for _, script := range queries.InsertScripts {
		TestScriptPrepared(t, harness, script)
	}
}

func TestComplexIndexQueriesPrepared(t *testing.T, harness Harness) {
	harness.Setup(setup.ComplexIndexSetup...)
	e := mustNewEngine(t, harness)
	defer e.Close()
	for _, tt := range queries.ComplexIndexQueries {
		TestPreparedQueryWithEngine(t, harness, e, tt)
	}
}

func TestJsonScriptsPrepared(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	for _, script := range queries.JsonScripts {
		TestScriptPrepared(t, harness, script)
	}
}

func TestCreateCheckConstraintsScriptsPrepared(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	for _, script := range queries.CreateCheckConstraintsScripts {
		TestScriptPrepared(t, harness, script)
	}
}

func TestInsertIgnoreScriptsPrepared(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	for _, script := range queries.InsertIgnoreScripts {
		TestScriptPrepared(t, harness, script)
	}
}

func TestInsertErrorScriptsPrepared(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	for _, script := range queries.InsertErrorScripts {
		TestScriptPrepared(t, harness, script)
	}
}

func TestUserPrivileges(t *testing.T, h Harness) {
	harness, ok := h.(ClientHarness)
	if !ok {
		t.Skip("Cannot run TestUserPrivileges as the harness must implement ClientHarness")
	}

	harness.Setup(setup.MydbData, setup.MytableData)
	for _, script := range queries.UserPrivTests {
		t.Run(script.Name, func(t *testing.T) {
			engine := mustNewEngine(t, harness)
			defer engine.Close()

			ctx := NewContext(harness)
			ctx.NewCtxWithClient(sql.Client{
				User:    "root",
				Address: "localhost",
			})
			engine.Analyzer.Catalog.MySQLDb.AddRootAccount()

			for _, statement := range script.SetUpScript {
				if sh, ok := harness.(SkippingHarness); ok {
					if sh.SkipQueryTest(statement) {
						t.Skip()
					}
				}
				RunQueryWithContext(t, engine, ctx, statement)
			}
			for _, assertion := range script.Assertions {
				if sh, ok := harness.(SkippingHarness); ok {
					if sh.SkipQueryTest(assertion.Query) {
						t.Skipf("Skipping query %s", assertion.Query)
					}
				}

				user := assertion.User
				host := assertion.Host
				if user == "" {
					user = "root"
				}
				if host == "" {
					host = "localhost"
				}
				ctx := NewContextWithClient(harness, sql.Client{
					User:    user,
					Address: host,
				})

				if assertion.ExpectedErr != nil {
					t.Run(assertion.Query, func(t *testing.T) {
						AssertErrWithCtx(t, engine, ctx, assertion.Query, assertion.ExpectedErr)
					})
				} else if assertion.ExpectedErrStr != "" {
					t.Run(assertion.Query, func(t *testing.T) {
						AssertErrWithCtx(t, engine, ctx, assertion.Query, nil, assertion.ExpectedErrStr)
					})
				} else {
					t.Run(assertion.Query, func(t *testing.T) {
						TestQueryWithContext(t, ctx, engine, assertion.Query, assertion.Expected, nil, nil)
					})
				}
			}
		})
	}

	// These tests are functionally identical to UserPrivTests, hence their inclusion in the same testing function.
	// They're just written a little differently to ease the developer's ability to produce as many as possible.

	harness.Setup([]setup.SetupScript{{
		"create database mydb",
		"create database otherdb",
	}})
	for _, script := range queries.QuickPrivTests {
		t.Run(strings.Join(script.Queries, "\n > "), func(t *testing.T) {
			engine := mustNewEngine(t, harness)
			defer engine.Close()

			engine.Analyzer.Catalog.MySQLDb.AddRootAccount()
			rootCtx := harness.NewContextWithClient(sql.Client{
				User:    "root",
				Address: "localhost",
			})
			rootCtx.SetCurrentDatabase("mydb")
			for _, setupQuery := range []string{
				"CREATE USER tester@localhost;",
				"CREATE TABLE mydb.test (pk BIGINT PRIMARY KEY, v1 BIGINT);",
				"CREATE TABLE mydb.test2 (pk BIGINT PRIMARY KEY, v1 BIGINT);",
				"CREATE TABLE otherdb.test (pk BIGINT PRIMARY KEY, v1 BIGINT);",
				"CREATE TABLE otherdb.test2 (pk BIGINT PRIMARY KEY, v1 BIGINT);",
				"INSERT INTO mydb.test VALUES (0, 0), (1, 1);",
				"INSERT INTO mydb.test2 VALUES (0, 1), (1, 2);",
				"INSERT INTO otherdb.test VALUES (1, 1), (2, 2);",
				"INSERT INTO otherdb.test2 VALUES (1, 1), (2, 2);",
			} {
				RunQueryWithContext(t, engine, rootCtx, setupQuery)
			}

			for i := 0; i < len(script.Queries)-1; i++ {
				if sh, ok := harness.(SkippingHarness); ok {
					if sh.SkipQueryTest(script.Queries[i]) {
						t.Skipf("Skipping query %s", script.Queries[i])
					}
				}
				RunQueryWithContext(t, engine, rootCtx, script.Queries[i])
			}
			lastQuery := script.Queries[len(script.Queries)-1]
			if sh, ok := harness.(SkippingHarness); ok {
				if sh.SkipQueryTest(lastQuery) {
					t.Skipf("Skipping query %s", lastQuery)
				}
			}
			ctx := rootCtx.NewCtxWithClient(sql.Client{
				User:    "tester",
				Address: "localhost",
			})
			ctx.SetCurrentDatabase(rootCtx.GetCurrentDatabase())
			if script.ExpectedErr != nil {
				t.Run(lastQuery, func(t *testing.T) {
					AssertErrWithCtx(t, engine, ctx, lastQuery, script.ExpectedErr)
				})
			} else if script.ExpectingErr {
				t.Run(lastQuery, func(t *testing.T) {
					sch, iter, err := engine.Query(ctx, lastQuery)
					if err == nil {
						_, err = sql.RowIterToRows(ctx, sch, iter)
					}
					require.Error(t, err)
					for _, errKind := range []*errors.Kind{
						sql.ErrPrivilegeCheckFailed,
						sql.ErrDatabaseAccessDeniedForUser,
						sql.ErrTableAccessDeniedForUser,
					} {
						if errKind.Is(err) {
							return
						}
					}
					t.Fatalf("Not a standard privilege-check error: %s", err.Error())
				})
			} else {
				t.Run(lastQuery, func(t *testing.T) {
					sch, iter, err := engine.Query(ctx, lastQuery)
					require.NoError(t, err)
					rows, err := sql.RowIterToRows(ctx, sch, iter)
					require.NoError(t, err)
					// See the comment on QuickPrivilegeTest for a more in-depth explanation, but essentially we treat
					// nil in script.Expected as matching "any" non-error result.
					if script.Expected != nil && (rows != nil || len(script.Expected) != 0) {
						checkResults(t, require.New(t), script.Expected, nil, sch, rows, lastQuery)
					}
				})
			}
		})
	}
}

func TestUserAuthentication(t *testing.T, h Harness) {
	harness, ok := h.(ClientHarness)
	if !ok {
		t.Skip("Cannot run TestUserAuthentication as the harness must implement ClientHarness")
	}
	harness.Setup(setup.MydbData, setup.MytableData)

	port := getEmptyPort(t)
	for _, script := range queries.ServerAuthTests {
		t.Run(script.Name, func(t *testing.T) {
			ctx := NewContextWithClient(harness, sql.Client{
				User:    "root",
				Address: "localhost",
			})
			serverConfig := server.Config{
				Protocol:       "tcp",
				Address:        fmt.Sprintf("localhost:%d", port),
				MaxConnections: 1000,
			}

			engine := mustNewEngine(t, harness)
			defer engine.Close()
			engine.Analyzer.Catalog.MySQLDb.AddRootAccount()
			if script.SetUpFunc != nil {
				script.SetUpFunc(ctx, t, engine)
			}
			for _, statement := range script.SetUpScript {
				if sh, ok := harness.(SkippingHarness); ok {
					if sh.SkipQueryTest(statement) {
						t.Skip()
					}
				}
				RunQueryWithContext(t, engine, ctx, statement)
			}

			s, err := server.NewDefaultServer(serverConfig, engine)
			require.NoError(t, err)
			go func() {
				err := s.Start()
				require.NoError(t, err)
			}()
			defer func() {
				require.NoError(t, s.Close())
			}()

			for _, assertion := range script.Assertions {
				conn, err := dbr.Open("mysql", fmt.Sprintf("%s:%s@tcp(localhost:%d)/",
					assertion.Username, assertion.Password, port), nil)
				require.NoError(t, err)
				if assertion.ExpectedErr {
					r, err := conn.Query(assertion.Query)
					if !assert.Error(t, err) {
						require.NoError(t, r.Close())
					}
				} else {
					r, err := conn.Query(assertion.Query)
					if assert.NoError(t, err) {
						require.NoError(t, r.Close())
					}
				}
				require.NoError(t, conn.Close())
			}
		})
	}
}

func TestComplexIndexQueries(t *testing.T, harness Harness) {
	harness.Setup(setup.ComplexIndexSetup...)
	e := mustNewEngine(t, harness)
	defer e.Close()
	for _, tt := range queries.ComplexIndexQueries {
		TestQueryWithEngine(t, harness, e, tt)
	}
}

func TestTriggers(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.FooData)
	for _, script := range queries.TriggerTests {
		TestScript(t, harness, script)
	}

	harness.Setup(setup.MydbData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	t.Run("no database selected", func(t *testing.T) {
		ctx := NewContext(harness)
		ctx.SetCurrentDatabase("")

		RunQueryWithContext(t, e, ctx, "create table mydb.a (i int primary key, j int)")
		RunQueryWithContext(t, e, ctx, "create table mydb.b (x int primary key)")

		TestQueryWithContext(t, ctx, e, "CREATE TRIGGER mydb.trig BEFORE INSERT ON mydb.a FOR EACH ROW BEGIN SET NEW.j = (SELECT COALESCE(MAX(x),1) FROM mydb.b); UPDATE mydb.b SET x = x + 1; END", []sql.Row{{sql.OkResult{}}}, nil, nil)

		RunQueryWithContext(t, e, ctx, "insert into mydb.b values (1)")
		RunQueryWithContext(t, e, ctx, "insert into mydb.a values (1,0), (2,0), (3,0)")

		TestQueryWithContext(t, ctx, e, "select * from mydb.a order by i", []sql.Row{{1, 1}, {2, 2}, {3, 3}}, nil, nil)

		TestQueryWithContext(t, ctx, e, "DROP TRIGGER mydb.trig", []sql.Row{}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SHOW TRIGGERS FROM mydb", []sql.Row{}, nil, nil)
	})
}

func TestRollbackTriggers(t *testing.T, harness Harness) {
	harness.Setup()
	for _, script := range queries.RollbackTriggerTests {
		TestScript(t, harness, script)
	}
}

func TestShowTriggers(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	e := mustNewEngine(t, harness)

	// Pick a date
	date := time.Unix(0, 0).UTC()

	// Set up Harness to contain triggers; created at a specific time
	var ctx *sql.Context
	setupTriggers := []struct {
		Query    string
		Expected []sql.Row
	}{
		{"create table a (x int primary key)", []sql.Row{{sql.NewOkResult(0)}}},
		{"create table b (y int primary key)", []sql.Row{{sql.NewOkResult(0)}}},
		{"create trigger a1 before insert on a for each row set new.x = New.x + 1", []sql.Row{{sql.NewOkResult(0)}}},
		{"create trigger a2 before insert on a for each row precedes a1 set new.x = New.x * 2", []sql.Row{{sql.NewOkResult(0)}}},
		{"create trigger a3 before insert on a for each row precedes a2 set new.x = New.x - 5", []sql.Row{{sql.NewOkResult(0)}}},
		{"create trigger a4 before insert on a for each row follows a2 set new.x = New.x * 3", []sql.Row{{sql.NewOkResult(0)}}},
		// order of execution should be: a3, a2, a4, a1
		{"create trigger a5 after insert on a for each row update b set y = y + 1 order by y asc", []sql.Row{{sql.NewOkResult(0)}}},
		{"create trigger a6 after insert on a for each row precedes a5 update b set y = y * 2 order by y asc", []sql.Row{{sql.NewOkResult(0)}}},
		{"create trigger a7 after insert on a for each row precedes a6 update b set y = y - 5 order by y asc", []sql.Row{{sql.NewOkResult(0)}}},
		{"create trigger a8 after insert on a for each row follows a6 update b set y = y * 3 order by y asc", []sql.Row{{sql.NewOkResult(0)}}},
		// order of execution should be: a7, a6, a8, a5
	}
	for _, tt := range setupTriggers {
		t.Run("setting up triggers", func(t *testing.T) {
			sql.RunWithNowFunc(func() time.Time { return date }, func() error {
				ctx = NewContext(harness)
				TestQueryWithContext(t, ctx, e, tt.Query, tt.Expected, nil, nil)
				return nil
			})
		})
	}

	// Test selecting these queries
	expectedResults := []struct {
		Query    string
		Expected []sql.Row
	}{
		{
			Query: "select * from information_schema.triggers",
			Expected: []sql.Row{
				{
					"def",                   // trigger_catalog
					"mydb",                  // trigger_schema
					"a1",                    // trigger_name
					"INSERT",                // event_manipulation
					"def",                   // event_object_catalog
					"mydb",                  // event_object_schema
					"a",                     // event_object_table
					int64(4),                // action_order
					nil,                     // action_condition
					"set new.x = New.x + 1", // action_statement
					"ROW",                   // action_orientation
					"BEFORE",                // action_timing
					nil,                     // action_reference_old_table
					nil,                     // action_reference_new_table
					"OLD",                   // action_reference_old_row
					"NEW",                   // action_reference_new_row
					date,                    // created
					"",                      // sql_mode
					"",                      // definer
					sql.Collation_Default.CharacterSet().String(), // character_set_client
					sql.Collation_Default.String(),                // collation_connection
					sql.Collation_Default.String(),                // database_collation
				},
				{
					"def",                   // trigger_catalog
					"mydb",                  // trigger_schema
					"a2",                    // trigger_name
					"INSERT",                // event_manipulation
					"def",                   // event_object_catalog
					"mydb",                  // event_object_schema
					"a",                     // event_object_table
					int64(2),                // action_order
					nil,                     // action_condition
					"set new.x = New.x * 2", // action_statement
					"ROW",                   // action_orientation
					"BEFORE",                // action_timing
					nil,                     // action_reference_old_table
					nil,                     // action_reference_new_table
					"OLD",                   // action_reference_old_row
					"NEW",                   // action_reference_new_row
					date,                    // created
					"",                      // sql_mode
					"",                      // definer
					sql.Collation_Default.CharacterSet().String(), // character_set_client
					sql.Collation_Default.String(),                // collation_connection
					sql.Collation_Default.String(),                // database_collation
				},
				{
					"def",                   // trigger_catalog
					"mydb",                  // trigger_schema
					"a3",                    // trigger_name
					"INSERT",                // event_manipulation
					"def",                   // event_object_catalog
					"mydb",                  // event_object_schema
					"a",                     // event_object_table
					int64(1),                // action_order
					nil,                     // action_condition
					"set new.x = New.x - 5", // action_statement
					"ROW",                   // action_orientation
					"BEFORE",                // action_timing
					nil,                     // action_reference_old_table
					nil,                     // action_reference_new_table
					"OLD",                   // action_reference_old_row
					"NEW",                   // action_reference_new_row
					date,                    // created
					"",                      // sql_mode
					"",                      // definer
					sql.Collation_Default.CharacterSet().String(), // character_set_client
					sql.Collation_Default.String(),                // collation_connection
					sql.Collation_Default.String(),                // database_collation
				},
				{
					"def",                   // trigger_catalog
					"mydb",                  // trigger_schema
					"a4",                    // trigger_name
					"INSERT",                // event_manipulation
					"def",                   // event_object_catalog
					"mydb",                  // event_object_schema
					"a",                     // event_object_table
					int64(3),                // action_order
					nil,                     // action_condition
					"set new.x = New.x * 3", // action_statement
					"ROW",                   // action_orientation
					"BEFORE",                // action_timing
					nil,                     // action_reference_old_table
					nil,                     // action_reference_new_table
					"OLD",                   // action_reference_old_row
					"NEW",                   // action_reference_new_row
					date,                    // created
					"",                      // sql_mode
					"",                      // definer
					sql.Collation_Default.CharacterSet().String(), // character_set_client
					sql.Collation_Default.String(),                // collation_connection
					sql.Collation_Default.String(),                // database_collation
				},
				{
					"def",                                   // trigger_catalog
					"mydb",                                  // trigger_schema
					"a5",                                    // trigger_name
					"INSERT",                                // event_manipulation
					"def",                                   // event_object_catalog
					"mydb",                                  // event_object_schema
					"a",                                     // event_object_table
					int64(4),                                // action_order
					nil,                                     // action_condition
					"update b set y = y + 1 order by y asc", // action_statement
					"ROW",                                   // action_orientation
					"AFTER",                                 // action_timing
					nil,                                     // action_reference_old_table
					nil,                                     // action_reference_new_table
					"OLD",                                   // action_reference_old_row
					"NEW",                                   // action_reference_new_row
					date,                                    // created
					"",                                      // sql_mode
					"",                                      // definer
					sql.Collation_Default.CharacterSet().String(), // character_set_client
					sql.Collation_Default.String(),                // collation_connection
					sql.Collation_Default.String(),                // database_collation
				},
				{
					"def",                                   // trigger_catalog
					"mydb",                                  // trigger_schema
					"a6",                                    // trigger_name
					"INSERT",                                // event_manipulation
					"def",                                   // event_object_catalog
					"mydb",                                  // event_object_schema
					"a",                                     // event_object_table
					int64(2),                                // action_order
					nil,                                     // action_condition
					"update b set y = y * 2 order by y asc", // action_statement
					"ROW",                                   // action_orientation
					"AFTER",                                 // action_timing
					nil,                                     // action_reference_old_table
					nil,                                     // action_reference_new_table
					"OLD",                                   // action_reference_old_row
					"NEW",                                   // action_reference_new_row
					date,                                    // created
					"",                                      // sql_mode
					"",                                      // definer
					sql.Collation_Default.CharacterSet().String(), // character_set_client
					sql.Collation_Default.String(),                // collation_connection
					sql.Collation_Default.String(),                // database_collation
				},
				{
					"def",                                   // trigger_catalog
					"mydb",                                  // trigger_schema
					"a7",                                    // trigger_name
					"INSERT",                                // event_manipulation
					"def",                                   // event_object_catalog
					"mydb",                                  // event_object_schema
					"a",                                     // event_object_table
					int64(1),                                // action_order
					nil,                                     // action_condition
					"update b set y = y - 5 order by y asc", // action_statement
					"ROW",                                   // action_orientation
					"AFTER",                                 // action_timing
					nil,                                     // action_reference_old_table
					nil,                                     // action_reference_new_table
					"OLD",                                   // action_reference_old_row
					"NEW",                                   // action_reference_new_row
					date,                                    // created
					"",                                      // sql_mode
					"",                                      // definer
					sql.Collation_Default.CharacterSet().String(), // character_set_client
					sql.Collation_Default.String(),                // collation_connection
					sql.Collation_Default.String(),                // database_collation
				},
				{
					"def",                                   // trigger_catalog
					"mydb",                                  // trigger_schema
					"a8",                                    // trigger_name
					"INSERT",                                // event_manipulation
					"def",                                   // event_object_catalog
					"mydb",                                  // event_object_schema
					"a",                                     // event_object_table
					int64(3),                                // action_order
					nil,                                     // action_condition
					"update b set y = y * 3 order by y asc", // action_statement
					"ROW",                                   // action_orientation
					"AFTER",                                 // action_timing
					nil,                                     // action_reference_old_table
					nil,                                     // action_reference_new_table
					"OLD",                                   // action_reference_old_row
					"NEW",                                   // action_reference_new_row
					date,                                    // created
					"",                                      // sql_mode
					"",                                      // definer
					sql.Collation_Default.CharacterSet().String(), // character_set_client
					sql.Collation_Default.String(),                // collation_connection
					sql.Collation_Default.String(),                // database_collation
				},
			},
		},
	}

	for _, tt := range expectedResults {
		t.Run(tt.Query, func(t *testing.T) {
			TestQueryWithContext(t, ctx, e, tt.Query, tt.Expected, nil, nil)
		})
	}
}

func TestStoredProcedures(t *testing.T, harness Harness) {
	for _, script := range queries.ProcedureLogicTests {
		TestScript(t, harness, script)
	}
	for _, script := range queries.ProcedureCallTests {
		TestScript(t, harness, script)
	}
	for _, script := range queries.ProcedureDropTests {
		TestScript(t, harness, script)
	}
	for _, script := range queries.ProcedureShowStatus {
		TestScript(t, harness, script)
	}
	for _, script := range queries.ProcedureShowCreate {
		TestScript(t, harness, script)
	}

	harness.Setup(setup.MydbData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	t.Run("no database selected", func(t *testing.T) {
		ctx := NewContext(harness)
		ctx.SetCurrentDatabase("")

		TestQueryWithContext(t, ctx, e, "CREATE PROCEDURE mydb.p1() SELECT 5", []sql.Row{{sql.OkResult{}}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "CREATE PROCEDURE mydb.p2() SELECT 6", []sql.Row{{sql.OkResult{}}}, nil, nil)

		TestQueryWithContext(t, ctx, e, "SHOW PROCEDURE STATUS", []sql.Row{
			{"mydb", "p1", "PROCEDURE", "", time.Unix(0, 0).UTC(), time.Unix(0, 0).UTC(),
				"DEFINER", "", "utf8mb4", "utf8mb4_0900_bin", "utf8mb4_0900_bin"},
			{"mydb", "p2", "PROCEDURE", "", time.Unix(0, 0).UTC(), time.Unix(0, 0).UTC(),
				"DEFINER", "", "utf8mb4", "utf8mb4_0900_bin", "utf8mb4_0900_bin"},
		}, nil, nil)

		TestQueryWithContext(t, ctx, e, "DROP PROCEDURE mydb.p1", []sql.Row{}, nil, nil)

		TestQueryWithContext(t, ctx, e, "SHOW PROCEDURE STATUS", []sql.Row{
			{"mydb", "p2", "PROCEDURE", "", time.Unix(0, 0).UTC(), time.Unix(0, 0).UTC(),
				"DEFINER", "", "utf8mb4", "utf8mb4_0900_bin", "utf8mb4_0900_bin"},
		}, nil, nil)
	})
}

func TestTriggerErrors(t *testing.T, harness Harness) {
	for _, script := range queries.TriggerErrorTests {
		TestScript(t, harness, script)
	}
}

// TestScript runs the test script given, making any assertions given
func TestScript(t *testing.T, harness Harness, script queries.ScriptTest) {
	e := mustNewEngine(t, harness)
	defer e.Close()
	TestScriptWithEngine(t, e, harness, script)
}

// TestScriptWithEngine runs the test script given with the engine provided.
func TestScriptWithEngine(t *testing.T, e *sqle.Engine, harness Harness, script queries.ScriptTest) {
	t.Run(script.Name, func(t *testing.T) {
		for _, statement := range script.SetUpScript {
			if sh, ok := harness.(SkippingHarness); ok {
				if sh.SkipQueryTest(statement) {
					t.Skip()
				}
			}

			RunQuery(t, e, harness, statement)
		}

		assertions := script.Assertions
		if len(assertions) == 0 {
			assertions = []queries.ScriptTestAssertion{
				{
					Query:       script.Query,
					Expected:    script.Expected,
					ExpectedErr: script.ExpectedErr,
				},
			}
		}

		for _, assertion := range assertions {
			if assertion.ExpectedErr != nil {
				t.Run(assertion.Query, func(t *testing.T) {
					AssertErr(t, e, harness, assertion.Query, assertion.ExpectedErr)
				})
			} else if assertion.ExpectedErrStr != "" {
				t.Run(assertion.Query, func(t *testing.T) {
					AssertErr(t, e, harness, assertion.Query, nil, assertion.ExpectedErrStr)
				})
			} else if assertion.ExpectedWarning != 0 {
				t.Run(assertion.Query, func(t *testing.T) {
					AssertWarningAndTestQuery(t, e, nil, harness, assertion.Query,
						assertion.Expected, nil, assertion.ExpectedWarning, assertion.ExpectedWarningsCount,
						assertion.ExpectedWarningMessageSubstring, assertion.SkipResultsCheck)
				})
			} else if assertion.SkipResultsCheck {
				RunQuery(t, e, harness, assertion.Query)
			} else {
				t.Run(assertion.Query, func(t *testing.T) {
					ctx := NewContext(harness)
					TestQueryWithContext(t, ctx, e, assertion.Query, assertion.Expected, nil, assertion.Bindings)
				})
			}
		}
	})
}

// TestScriptPrepared substitutes literals for bindvars, runs the test script given,
// and makes any assertions given
func TestScriptPrepared(t *testing.T, harness Harness, script queries.ScriptTest) bool {
	return t.Run(script.Name, func(t *testing.T) {
		if script.SkipPrepared {
			t.Skip()
		}

		e := mustNewEngine(t, harness)
		defer e.Close()
		TestScriptWithEnginePrepared(t, e, harness, script)
	})
}

// TestScriptWithEnginePrepared runs the test script with bindvars substituted for literals
// using the engine provided.
func TestScriptWithEnginePrepared(t *testing.T, e *sqle.Engine, harness Harness, script queries.ScriptTest) {
	ctx := NewContextWithEngine(harness, e)
	for _, statement := range script.SetUpScript {
		if sh, ok := harness.(SkippingHarness); ok {
			if sh.SkipQueryTest(statement) {
				t.Skip()
			}
		}
		_, _, err := runQueryPreparedWithCtx(t, ctx, e, statement)
		require.NoError(t, err)
	}

	assertions := script.Assertions
	if len(assertions) == 0 {
		assertions = []queries.ScriptTestAssertion{
			{
				Query:       script.Query,
				Expected:    script.Expected,
				ExpectedErr: script.ExpectedErr,
			},
		}
	}

	for _, assertion := range assertions {
		if sh, ok := harness.(SkippingHarness); ok {
			if sh.SkipQueryTest(assertion.Query) {
				t.Skip()
			}
		}
		if assertion.ExpectedErr != nil {
			t.Run(assertion.Query, func(t *testing.T) {
				AssertErr(t, e, harness, assertion.Query, assertion.ExpectedErr)
			})
		} else if assertion.ExpectedErrStr != "" {
			t.Run(assertion.Query, func(t *testing.T) {
				AssertErr(t, e, harness, assertion.Query, nil, assertion.ExpectedErrStr)
			})
		} else if assertion.ExpectedWarning != 0 {
			t.Run(assertion.Query, func(t *testing.T) {
				AssertWarningAndTestQuery(t, e, nil, harness, assertion.Query,
					assertion.Expected, nil, assertion.ExpectedWarning, assertion.ExpectedWarningsCount,
					assertion.ExpectedWarningMessageSubstring, assertion.SkipResultsCheck)
			})
		} else {
			TestPreparedQueryWithContext(t, ctx, e, assertion.Query, assertion.Expected, nil)
		}
	}
}

func TestTransactionScripts(t *testing.T, harness Harness) {
	for _, script := range queries.TransactionTests {
		TestTransactionScript(t, harness, script)
	}
}

// TestTransactionScript runs the test script given, making any assertions given
func TestTransactionScript(t *testing.T, harness Harness, script queries.TransactionTest) bool {
	// todo(max): these use dolt_commit, need harness reset to reset back to original commit
	return t.Run(script.Name, func(t *testing.T) {
		harness.Setup(setup.MydbData)
		e := mustNewEngine(t, harness)
		defer e.Close()
		TestTransactionScriptWithEngine(t, e, harness, script)
	})
}

// TestTransactionScriptWithEngine runs the transaction test script given with the engine provided.
func TestTransactionScriptWithEngine(t *testing.T, e *sqle.Engine, harness Harness, script queries.TransactionTest) {
	setupSession := NewSession(harness)
	for _, statement := range script.SetUpScript {
		RunQueryWithContext(t, e, setupSession, statement)
	}

	clientSessions := make(map[string]*sql.Context)
	assertions := script.Assertions

	for _, assertion := range assertions {
		client := getClient(assertion.Query)

		clientSession, ok := clientSessions[client]
		if !ok {
			clientSession = NewSession(harness)
			clientSessions[client] = clientSession
		}

		t.Run(assertion.Query, func(t *testing.T) {
			if assertion.ExpectedErr != nil {
				AssertErrWithCtx(t, e, clientSession, assertion.Query, assertion.ExpectedErr)
			} else if assertion.ExpectedErrStr != "" {
				AssertErrWithCtx(t, e, clientSession, assertion.Query, nil, assertion.ExpectedErrStr)
			} else if assertion.ExpectedWarning != 0 {
				AssertWarningAndTestQuery(t, e, nil, harness, assertion.Query, assertion.Expected,
					nil, assertion.ExpectedWarning, assertion.ExpectedWarningsCount,
					assertion.ExpectedWarningMessageSubstring, false)
			} else if assertion.SkipResultsCheck {
				RunQueryWithContext(t, e, clientSession, assertion.Query)
			} else {
				TestQueryWithContext(t, clientSession, e, assertion.Query, assertion.Expected, nil, nil)
			}
		})
	}
}

func getClient(query string) string {
	startCommentIdx := strings.Index(query, "/*")
	endCommentIdx := strings.Index(query, "*/")
	if startCommentIdx < 0 || endCommentIdx < 0 {
		panic("no client comment found in query " + query)
	}

	query = query[startCommentIdx+2 : endCommentIdx]
	if strings.Index(query, "client ") < 0 {
		panic("no client comment found in query " + query)
	}

	return strings.TrimSpace(strings.TrimPrefix(query, "client"))
}

func TestViews(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	// nested views
	RunQueryWithContext(t, e, ctx, "CREATE VIEW myview2 AS SELECT * FROM myview WHERE i = 1")
	for _, testCase := range queries.ViewTests {
		t.Run(testCase.Query, func(t *testing.T) {
			TestQueryWithContext(t, ctx, e, testCase.Query, testCase.Expected, nil, nil)
		})
	}

	// Views with non-standard select statements
	RunQueryWithContext(t, e, ctx, "create view unionView as (select * from myTable order by i limit 1) union all (select * from mytable order by i limit 1)")
	t.Run("select * from unionview order by i", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "select * from unionview order by i", []sql.Row{
			{1, "first row"},
			{1, "first row"},
		}, nil, nil)
	})

	t.Run("create view with algorithm, definer, security defined", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`localhost` SQL SECURITY DEFINER VIEW newview AS SELECT * FROM myview WHERE i = 1", []sql.Row{}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM newview ORDER BY i", []sql.Row{
			sql.NewRow(int64(1), "first row"),
		}, nil, nil)

		TestQueryWithContext(t, ctx, e, "CREATE OR REPLACE ALGORITHM=MERGE DEFINER=doltUser SQL SECURITY INVOKER VIEW newview AS SELECT * FROM myview WHERE i = 2", []sql.Row{}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM newview ORDER BY i", []sql.Row{
			sql.NewRow(int64(2), "second row"),
		}, nil, nil)
	})
}

func TestViewsPrepared(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	RunQueryWithContext(t, e, ctx, "CREATE VIEW myview2 AS SELECT * FROM myview WHERE i = 1")
	for _, testCase := range queries.ViewTests {
		TestPreparedQueryWithEngine(t, harness, e, testCase)
	}
}

func TestVersionedViews(t *testing.T, harness Harness) {
	if _, ok := harness.(VersionedDBHarness); !ok {
		t.Skipf("Skipping versioned test, harness doesn't implement VersionedDBHarness")
	}

	require := require.New(t)

	e := NewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)
	_, iter, err := e.Query(ctx, "CREATE VIEW myview1 AS SELECT * FROM myhistorytable")
	require.NoError(err)
	iter.Close(ctx)

	// nested views
	_, iter, err = e.Query(ctx, "CREATE VIEW myview2 AS SELECT * FROM myview1 WHERE i = 1")
	require.NoError(err)
	iter.Close(ctx)

	for _, testCase := range queries.VersionedViewTests {
		ctx := NewContext(harness)
		TestQueryWithContext(t, ctx, e, testCase.Query, testCase.Expected, testCase.ExpectedColumns, nil)
	}
}

func TestVersionedViewsPrepared(t *testing.T, harness Harness) {
	if _, ok := harness.(VersionedDBHarness); !ok {
		t.Skipf("Skipping versioned test, harness doesn't implement VersionedDBHarness")
	}

	require := require.New(t)

	e := NewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)
	_, iter, err := e.Query(ctx, "CREATE VIEW myview1 AS SELECT * FROM myhistorytable")
	require.NoError(err)
	iter.Close(ctx)

	// nested views
	_, iter, err = e.Query(ctx, "CREATE VIEW myview2 AS SELECT * FROM myview1 WHERE i = 1")
	require.NoError(err)
	iter.Close(ctx)

	for _, testCase := range queries.VersionedViewTests {
		TestPreparedQueryWithEngine(t, harness, e, testCase)
	}
}

func TestCreateTable(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData, setup.FooData)
	for _, tt := range queries.CreateTableQueries {
		runWriteQueryTest(t, harness, tt)
	}

	harness.Setup(setup.MydbData, setup.MytableData)
	e := mustNewEngine(t, harness)
	defer e.Close()

	t.Run("no database selected", func(t *testing.T) {
		ctx := NewContext(harness)
		ctx.SetCurrentDatabase("")

		TestQueryWithContext(t, ctx, e, "CREATE TABLE mydb.t11 (a INTEGER NOT NULL PRIMARY KEY, "+
			"b VARCHAR(10) NOT NULL)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		db, err := e.Analyzer.Catalog.Database(ctx, "mydb")
		require.NoError(t, err)

		testTable, ok, err := db.GetTableInsensitive(ctx, "t11")
		require.NoError(t, err)
		require.True(t, ok)

		s := sql.Schema{
			{Name: "a", Type: sql.Int32, Nullable: false, PrimaryKey: true, Source: "t11"},
			{Name: "b", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 10), Nullable: false, Source: "t11"},
		}

		require.Equal(t, s, testTable.Schema())
	})

	t.Run("CREATE TABLE with multiple unnamed indexes", func(t *testing.T) {
		ctx := NewContext(harness)
		ctx.SetCurrentDatabase("")

		TestQueryWithContext(t, ctx, e, "CREATE TABLE mydb.t12 (a INTEGER NOT NULL PRIMARY KEY, "+
			"b VARCHAR(10) UNIQUE, c varchar(10) UNIQUE)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		db, err := e.Analyzer.Catalog.Database(ctx, "mydb")
		require.NoError(t, err)

		t12Table, ok, err := db.GetTableInsensitive(ctx, "t12")
		require.NoError(t, err)
		require.True(t, ok)

		t9TableIndexable, ok := t12Table.(sql.IndexedTable)
		require.True(t, ok)
		t9Indexes, err := t9TableIndexable.GetIndexes(ctx)
		require.NoError(t, err)
		uniqueCount := 0
		for _, index := range t9Indexes {
			if index.IsUnique() {
				uniqueCount += 1
			}
		}

		// We want two unique indexes to be created with unique names being generated. It is up to the integrator
		// to decide how empty string indexes are created. Adding in the primary key gives us a result of 3.
		require.Equal(t, 3, uniqueCount)

		// Validate No Unique Index has an empty Name
		for _, index := range t9Indexes {
			require.True(t, index.ID() != "")
		}
	})

	t.Run("create table with blob column with null default", func(t *testing.T) {
		ctx := NewContext(harness)
		ctx.SetCurrentDatabase("mydb")
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t_blob_default_null(c BLOB DEFAULT NULL)",
			[]sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		RunQuery(t, e, harness, "INSERT INTO t_blob_default_null VALUES ()")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t_blob_default_null",
			[]sql.Row{{nil}}, nil, nil)
	})
}

func TestDropTable(t *testing.T, harness Harness) {
	require := require.New(t)

	harness.Setup(setup.MydbData, setup.MytableData, setup.OthertableData, setup.TabletestData, setup.Pk_tablesData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)
	db, err := e.Analyzer.Catalog.Database(ctx, "mydb")
	require.NoError(err)

	_, ok, err := db.GetTableInsensitive(ctx, "mytable")
	require.True(ok)

	TestQueryWithContext(t, ctx, e, "DROP TABLE IF EXISTS mytable, not_exist", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

	_, ok, err = db.GetTableInsensitive(ctx, "mytable")
	require.NoError(err)
	require.False(ok)

	_, ok, err = db.GetTableInsensitive(ctx, "othertable")
	require.NoError(err)
	require.True(ok)

	_, ok, err = db.GetTableInsensitive(ctx, "tabletest")
	require.NoError(err)
	require.True(ok)

	TestQueryWithContext(t, ctx, e, "DROP TABLE IF EXISTS othertable, tabletest", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

	_, ok, err = db.GetTableInsensitive(ctx, "othertable")
	require.NoError(err)
	require.False(ok)

	_, ok, err = db.GetTableInsensitive(ctx, "tabletest")
	require.NoError(err)
	require.False(ok)

	_, _, err = e.Query(NewContext(harness), "DROP TABLE not_exist")
	require.Error(err)

	_, _, err = e.Query(NewContext(harness), "DROP TABLE IF EXISTS not_exist")
	require.NoError(err)

	t.Run("no database selected", func(t *testing.T) {
		ctx := NewContext(harness)
		ctx.SetCurrentDatabase("")

		RunQuery(t, e, harness, "CREATE DATABASE otherdb")
		otherdb, err := e.Analyzer.Catalog.Database(ctx, "otherdb")

		TestQueryWithContext(t, ctx, e, "DROP TABLE mydb.one_pk", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		_, ok, err = db.GetTableInsensitive(ctx, "mydb.one_pk")
		require.NoError(err)
		require.False(ok)

		RunQuery(t, e, harness, "CREATE TABLE otherdb.table1 (pk1 integer primary key)")
		RunQuery(t, e, harness, "CREATE TABLE otherdb.table2 (pk2 integer primary key)")

		_, _, err = e.Query(ctx, "DROP TABLE otherdb.table1, mydb.one_pk_two_idx")
		require.Error(err)

		_, ok, err = otherdb.GetTableInsensitive(ctx, "table1")
		require.NoError(err)
		require.True(ok)

		_, ok, err = db.GetTableInsensitive(ctx, "one_pk_two_idx")
		require.NoError(err)
		require.True(ok)

		_, _, err = e.Query(ctx, "DROP TABLE IF EXISTS otherdb.table1, mydb.one_pk")
		require.Error(err)

		_, ok, err = otherdb.GetTableInsensitive(ctx, "table1")
		require.NoError(err)
		require.True(ok)

		_, ok, err = db.GetTableInsensitive(ctx, "one_pk_two_idx")
		require.NoError(err)
		require.True(ok)

		_, _, err = e.Query(ctx, "DROP TABLE otherdb.table1, otherdb.table3")
		require.Error(err)

		_, ok, err = otherdb.GetTableInsensitive(ctx, "table1")
		require.NoError(err)
		require.True(ok)

		_, _, err = e.Query(ctx, "DROP TABLE IF EXISTS otherdb.table1, otherdb.table3")
		require.NoError(err)

		_, ok, err = otherdb.GetTableInsensitive(ctx, "table1")
		require.NoError(err)
		require.False(ok)
	})

	t.Run("cur database selected, drop tables in other db", func(t *testing.T) {
		ctx := NewContext(harness)
		ctx.SetCurrentDatabase("mydb")

		RunQuery(t, e, harness, "DROP DATABASE IF EXISTS otherdb")
		RunQuery(t, e, harness, "CREATE DATABASE otherdb")
		otherdb, err := e.Analyzer.Catalog.Database(ctx, "otherdb")

		RunQuery(t, e, harness, "CREATE TABLE tab1 (pk1 integer primary key, c1 text)")
		RunQuery(t, e, harness, "CREATE TABLE otherdb.tab1 (other_pk1 integer primary key)")
		RunQuery(t, e, harness, "CREATE TABLE otherdb.tab2 (other_pk2 integer primary key)")

		_, _, err = e.Query(ctx, "DROP TABLE otherdb.tab1")
		require.NoError(err)

		_, ok, err = db.GetTableInsensitive(ctx, "tab1")
		require.NoError(err)
		require.True(ok)

		_, ok, err = otherdb.GetTableInsensitive(ctx, "tab1")
		require.NoError(err)
		require.False(ok)

		_, _, err = e.Query(ctx, "DROP TABLE nonExistentTable, otherdb.tab2")
		require.Error(err)

		_, _, err = e.Query(ctx, "DROP TABLE IF EXISTS nonExistentTable, otherdb.tab2")
		require.Error(err)

		_, ok, err = otherdb.GetTableInsensitive(ctx, "tab2")
		require.NoError(err)
		require.True(ok)

		_, _, err = e.Query(ctx, "DROP TABLE IF EXISTS otherdb.tab3, otherdb.tab2")
		require.NoError(err)

		_, ok, err = otherdb.GetTableInsensitive(ctx, "tab2")
		require.NoError(err)
		require.False(ok)
	})
}

func TestRenameTable(t *testing.T, harness Harness) {
	require := require.New(t)
	harness.Setup(setup.MydbData, setup.MytableData, setup.OthertableData, setup.NiltableData, setup.EmptytableData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	db, err := e.Analyzer.Catalog.Database(NewContext(harness), "mydb")
	require.NoError(err)

	_, ok, err := db.GetTableInsensitive(NewContext(harness), "mytable")
	require.NoError(err)
	require.True(ok)

	TestQueryWithContext(t, ctx, e, "RENAME TABLE mytable TO newTableName", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

	_, ok, err = db.GetTableInsensitive(NewContext(harness), "mytable")
	require.NoError(err)
	require.False(ok)

	_, ok, err = db.GetTableInsensitive(NewContext(harness), "newTableName")
	require.NoError(err)
	require.True(ok)

	TestQueryWithContext(t, ctx, e, "RENAME TABLE othertable to othertable2, newTableName to mytable", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

	_, ok, err = db.GetTableInsensitive(NewContext(harness), "othertable")
	require.NoError(err)
	require.False(ok)

	_, ok, err = db.GetTableInsensitive(NewContext(harness), "othertable2")
	require.NoError(err)
	require.True(ok)

	_, ok, err = db.GetTableInsensitive(NewContext(harness), "newTableName")
	require.NoError(err)
	require.False(ok)

	_, ok, err = db.GetTableInsensitive(NewContext(harness), "mytable")
	require.NoError(err)
	require.True(ok)

	TestQueryWithContext(t, ctx, e, "ALTER TABLE mytable RENAME newTableName", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

	_, ok, err = db.GetTableInsensitive(NewContext(harness), "mytable")
	require.NoError(err)
	require.False(ok)

	_, ok, err = db.GetTableInsensitive(NewContext(harness), "newTableName")
	require.NoError(err)
	require.True(ok)

	_, _, err = e.Query(NewContext(harness), "ALTER TABLE not_exist RENAME foo")
	require.Error(err)
	require.True(sql.ErrTableNotFound.Is(err))

	_, _, err = e.Query(NewContext(harness), "ALTER TABLE emptytable RENAME niltable")
	require.Error(err)
	require.True(sql.ErrTableAlreadyExists.Is(err))

	t.Run("no database selected", func(t *testing.T) {
		ctx := NewContext(harness)
		ctx.SetCurrentDatabase("")

		t.Skip("broken")
		TestQueryWithContext(t, ctx, e, "RENAME TABLE mydb.emptytable TO mydb.emptytable2", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		_, ok, err = db.GetTableInsensitive(NewContext(harness), "emptytable")
		require.NoError(err)
		require.False(ok)

		_, ok, err = db.GetTableInsensitive(NewContext(harness), "emptytable2")
		require.NoError(err)
		require.True(ok)

		_, _, err = e.Query(NewContext(harness), "RENAME TABLE mydb.emptytable2 TO emptytable3")
		require.Error(err)
		require.True(sql.ErrNoDatabaseSelected.Is(err))
	})
}

func TestRenameColumn(t *testing.T, harness Harness) {
	require := require.New(t)

	harness.Setup(setup.MydbData, setup.MytableData, setup.TabletestData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)
	db, err := e.Analyzer.Catalog.Database(NewContext(harness), "mydb")
	require.NoError(err)

	// Error cases
	AssertErr(t, e, harness, "ALTER TABLE mytable RENAME COLUMN i2 TO iX", sql.ErrTableColumnNotFound)
	AssertErr(t, e, harness, "ALTER TABLE mytable RENAME COLUMN i TO iX, RENAME COLUMN iX TO i2", sql.ErrTableColumnNotFound)
	AssertErr(t, e, harness, "ALTER TABLE mytable RENAME COLUMN i TO iX, RENAME COLUMN i TO i2", sql.ErrTableColumnNotFound)
	AssertErr(t, e, harness, "ALTER TABLE mytable RENAME COLUMN i TO S", sql.ErrColumnExists)
	AssertErr(t, e, harness, "ALTER TABLE mytable RENAME COLUMN i TO n, RENAME COLUMN s TO N", sql.ErrColumnExists)

	tbl, ok, err := db.GetTableInsensitive(NewContext(harness), "mytable")
	require.NoError(err)
	require.True(ok)
	require.Equal(sql.Schema{
		{Name: "i", Type: sql.Int64, Source: "mytable", PrimaryKey: true},
		{Name: "s", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 20), Source: "mytable", Comment: "column s"},
	}, tbl.Schema())

	RunQuery(t, e, harness, "ALTER TABLE mytable RENAME COLUMN i TO i2, RENAME COLUMN s TO s2")
	tbl, ok, err = db.GetTableInsensitive(NewContext(harness), "mytable")
	require.NoError(err)
	require.True(ok)
	require.Equal(sql.Schema{
		{Name: "i2", Type: sql.Int64, Source: "mytable", PrimaryKey: true},
		{Name: "s2", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 20), Source: "mytable", Comment: "column s"},
	}, tbl.Schema())

	TestQueryWithContext(t, ctx, e, "select * from mytable order by i2 limit 1", []sql.Row{
		{1, "first row"},
	}, nil, nil)

	t.Run("rename column preserves table checks", func(t *testing.T) {
		RunQuery(t, e, harness, "ALTER TABLE mytable ADD CONSTRAINT test_check CHECK (i2 < 12345)")

		AssertErr(t, e, harness, "ALTER TABLE mytable RENAME COLUMN i2 TO i3", sql.ErrCheckConstraintInvalidatedByColumnAlter)

		RunQuery(t, e, harness, "ALTER TABLE mytable RENAME COLUMN s2 TO s3")
		tbl, ok, err = db.GetTableInsensitive(NewContext(harness), "mytable")
		require.NoError(err)
		require.True(ok)

		checkTable, ok := tbl.(sql.CheckTable)
		require.True(ok)
		checks, err := checkTable.GetChecks(NewContext(harness))
		require.NoError(err)
		require.Equal(1, len(checks))
		require.Equal("test_check", checks[0].Name)
		require.Equal("(i2 < 12345)", checks[0].CheckExpression)
	})

	t.Run("no database selected", func(t *testing.T) {
		ctx := NewContext(harness)
		ctx.SetCurrentDatabase("")

		beforeDropTbl, _, _ := db.GetTableInsensitive(NewContext(harness), "tabletest")

		TestQueryWithContext(t, ctx, e, "ALTER TABLE mydb.tabletest RENAME COLUMN s TO i1", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		tbl, ok, err = db.GetTableInsensitive(NewContext(harness), "tabletest")
		require.NoError(err)
		require.True(ok)
		assert.NotEqual(t, beforeDropTbl, tbl.Schema())
		assert.Equal(t, sql.Schema{
			{Name: "i", Type: sql.Int32, Source: "tabletest", PrimaryKey: true},
			{Name: "i1", Type: sql.Text, Source: "tabletest"},
		}, tbl.Schema())
	})
}

func assertSchemasEqualWithDefaults(t *testing.T, expected, actual sql.Schema) bool {
	if len(expected) != len(actual) {
		return assert.Equal(t, expected, actual)
	}

	ec, ac := make(sql.Schema, len(expected)), make(sql.Schema, len(actual))
	for i := range expected {
		ecc := *expected[i]
		acc := *actual[i]

		ecc.Default = nil
		acc.Default = nil

		ac[i] = &acc
		ec[i] = &ecc

		// For the default, compare just the string representations. This makes it possible for integrators who don't reify
		// default value expressions at schema load time (best practice) to run these tests. We also trim off any parens
		// for the same reason.
		eds, ads := "NULL", "NULL"
		if expected[i].Default != nil {
			eds = strings.Trim(expected[i].Default.String(), "()")
		}
		if actual[i].Default != nil {
			ads = strings.Trim(actual[i].Default.String(), "()")
		}

		assert.Equal(t, eds, ads, "column default values differ")
	}

	return assert.Equal(t, ec, ac)
}

//todo(max): convert to WriteQueryTest
func TestAddColumn(t *testing.T, harness Harness) {
	require := require.New(t)

	harness.Setup(setup.MydbData, setup.MytableData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)
	db, err := e.Analyzer.Catalog.Database(NewContext(harness), "mydb")
	require.NoError(err)

	t.Run("column at end with default", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "ALTER TABLE mytable ADD COLUMN i2 INT COMMENT 'hello' default 42", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		tbl, ok, err := db.GetTableInsensitive(NewContext(harness), "mytable")
		require.NoError(err)
		require.True(ok)
		assertSchemasEqualWithDefaults(t, sql.Schema{
			{Name: "i", Type: sql.Int64, Source: "mytable", PrimaryKey: true},
			{Name: "s", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 20), Source: "mytable", Comment: "column s"},
			{Name: "i2", Type: sql.Int32, Source: "mytable", Comment: "hello", Nullable: true, Default: parse.MustStringToColumnDefaultValue(NewContext(harness), "42", sql.Int32, true)},
		}, tbl.Schema())

		TestQueryWithContext(t, ctx, e, "SELECT * FROM mytable ORDER BY i", []sql.Row{
			sql.NewRow(int64(1), "first row", int32(42)),
			sql.NewRow(int64(2), "second row", int32(42)),
			sql.NewRow(int64(3), "third row", int32(42)),
		}, nil, nil)

	})

	t.Run("in middle, no default", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "ALTER TABLE mytable ADD COLUMN s2 TEXT COMMENT 'hello' AFTER i", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		tbl, ok, err := db.GetTableInsensitive(NewContext(harness), "mytable")
		require.NoError(err)
		require.True(ok)
		assertSchemasEqualWithDefaults(t, sql.Schema{
			{Name: "i", Type: sql.Int64, Source: "mytable", PrimaryKey: true},
			{Name: "s2", Type: sql.Text, Source: "mytable", Comment: "hello", Nullable: true},
			{Name: "s", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 20), Source: "mytable", Comment: "column s"},
			{Name: "i2", Type: sql.Int32, Source: "mytable", Comment: "hello", Nullable: true, Default: parse.MustStringToColumnDefaultValue(NewContext(harness), "42", sql.Int32, true)},
		}, tbl.Schema())

		TestQueryWithContext(t, ctx, e, "SELECT * FROM mytable ORDER BY i", []sql.Row{
			sql.NewRow(int64(1), nil, "first row", int32(42)),
			sql.NewRow(int64(2), nil, "second row", int32(42)),
			sql.NewRow(int64(3), nil, "third row", int32(42)),
		}, nil, nil)

		TestQueryWithContext(t, ctx, e, "insert into mytable values (4, 's2', 'fourth row', 11)", []sql.Row{
			{sql.NewOkResult(1)},
		}, nil, nil)
		TestQueryWithContext(t, ctx, e, "update mytable set s2 = 'updated s2' where i2 = 42", []sql.Row{
			{sql.OkResult{RowsAffected: 3, Info: plan.UpdateInfo{
				Matched: 3, Updated: 3,
			}}},
		}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM mytable ORDER BY i", []sql.Row{
			sql.NewRow(int64(1), "updated s2", "first row", int32(42)),
			sql.NewRow(int64(2), "updated s2", "second row", int32(42)),
			sql.NewRow(int64(3), "updated s2", "third row", int32(42)),
			sql.NewRow(int64(4), "s2", "fourth row", int32(11)),
		}, nil, nil)
	})

	t.Run("first with default", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "ALTER TABLE mytable ADD COLUMN s3 VARCHAR(25) COMMENT 'hello' default 'yay' FIRST", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		tbl, ok, err := db.GetTableInsensitive(NewContext(harness), "mytable")
		require.NoError(err)
		require.True(ok)
		assertSchemasEqualWithDefaults(t, sql.Schema{
			{Name: "s3", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 25), Source: "mytable", Comment: "hello", Nullable: true, Default: parse.MustStringToColumnDefaultValue(NewContext(harness), `"yay"`, sql.MustCreateStringWithDefaults(sqltypes.VarChar, 25), true)},
			{Name: "i", Type: sql.Int64, Source: "mytable", PrimaryKey: true},
			{Name: "s2", Type: sql.Text, Source: "mytable", Comment: "hello", Nullable: true},
			{Name: "s", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 20), Source: "mytable", Comment: "column s"},
			{Name: "i2", Type: sql.Int32, Source: "mytable", Comment: "hello", Nullable: true, Default: parse.MustStringToColumnDefaultValue(NewContext(harness), "42", sql.Int32, true)},
		}, tbl.Schema())

		TestQueryWithContext(t, ctx, e, "SELECT * FROM mytable ORDER BY i", []sql.Row{
			sql.NewRow("yay", int64(1), "updated s2", "first row", int32(42)),
			sql.NewRow("yay", int64(2), "updated s2", "second row", int32(42)),
			sql.NewRow("yay", int64(3), "updated s2", "third row", int32(42)),
			sql.NewRow("yay", int64(4), "s2", "fourth row", int32(11)),
		}, nil, nil)
	})

	t.Run("middle, no default, non null", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "ALTER TABLE mytable ADD COLUMN s4 VARCHAR(1) not null after s3", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		tbl, ok, err := db.GetTableInsensitive(NewContext(harness), "mytable")
		require.NoError(err)
		require.True(ok)
		assertSchemasEqualWithDefaults(t, sql.Schema{
			{Name: "s3", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 25), Source: "mytable", Comment: "hello", Nullable: true, Default: parse.MustStringToColumnDefaultValue(NewContext(harness), `"yay"`, sql.MustCreateStringWithDefaults(sqltypes.VarChar, 25), true)},
			{Name: "s4", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 1), Source: "mytable"},
			{Name: "i", Type: sql.Int64, Source: "mytable", PrimaryKey: true},
			{Name: "s2", Type: sql.Text, Source: "mytable", Comment: "hello", Nullable: true},
			{Name: "s", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 20), Source: "mytable", Comment: "column s"},
			{Name: "i2", Type: sql.Int32, Source: "mytable", Comment: "hello", Nullable: true, Default: parse.MustStringToColumnDefaultValue(NewContext(harness), "42", sql.Int32, true)},
		}, tbl.Schema())

		TestQueryWithContext(t, ctx, e, "SELECT * FROM mytable ORDER BY i", []sql.Row{
			sql.NewRow("yay", "", int64(1), "updated s2", "first row", int32(42)),
			sql.NewRow("yay", "", int64(2), "updated s2", "second row", int32(42)),
			sql.NewRow("yay", "", int64(3), "updated s2", "third row", int32(42)),
			sql.NewRow("yay", "", int64(4), "s2", "fourth row", int32(11)),
		}, nil, nil)
	})

	t.Run("multiple in one statement", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "ALTER TABLE mytable ADD COLUMN s5 VARCHAR(26), ADD COLUMN s6 VARCHAR(27)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		tbl, ok, err := db.GetTableInsensitive(NewContext(harness), "mytable")
		require.NoError(err)
		require.True(ok)
		assertSchemasEqualWithDefaults(t, sql.Schema{
			{Name: "s3", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 25), Source: "mytable", Comment: "hello", Nullable: true, Default: parse.MustStringToColumnDefaultValue(NewContext(harness), `"yay"`, sql.MustCreateStringWithDefaults(sqltypes.VarChar, 25), true)},
			{Name: "s4", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 1), Source: "mytable"},
			{Name: "i", Type: sql.Int64, Source: "mytable", PrimaryKey: true},
			{Name: "s2", Type: sql.Text, Source: "mytable", Comment: "hello", Nullable: true},
			{Name: "s", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 20), Source: "mytable", Comment: "column s"},
			{Name: "i2", Type: sql.Int32, Source: "mytable", Comment: "hello", Nullable: true, Default: parse.MustStringToColumnDefaultValue(NewContext(harness), "42", sql.Int32, true)},
			{Name: "s5", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 26), Source: "mytable", Nullable: true},
			{Name: "s6", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 27), Source: "mytable", Nullable: true},
		}, tbl.Schema())

		TestQueryWithContext(t, ctx, e, "SELECT * FROM mytable ORDER BY i", []sql.Row{
			sql.NewRow("yay", "", int64(1), "updated s2", "first row", int32(42), nil, nil),
			sql.NewRow("yay", "", int64(2), "updated s2", "second row", int32(42), nil, nil),
			sql.NewRow("yay", "", int64(3), "updated s2", "third row", int32(42), nil, nil),
			sql.NewRow("yay", "", int64(4), "s2", "fourth row", int32(11), nil, nil),
		}, nil, nil)
	})

	t.Run("error cases", func(t *testing.T) {
		AssertErr(t, e, harness, "ALTER TABLE not_exist ADD COLUMN i2 INT COMMENT 'hello'", sql.ErrTableNotFound)
		AssertErr(t, e, harness, "ALTER TABLE mytable ADD COLUMN b BIGINT COMMENT 'ok' AFTER not_exist", sql.ErrTableColumnNotFound)
		AssertErr(t, e, harness, "ALTER TABLE mytable ADD COLUMN i BIGINT COMMENT 'ok'", sql.ErrColumnExists)
		AssertErr(t, e, harness, "ALTER TABLE mytable ADD COLUMN b INT NOT NULL DEFAULT 'yes'", sql.ErrIncompatibleDefaultType)
		AssertErr(t, e, harness, "ALTER TABLE mytable ADD COLUMN c int, add c int", sql.ErrColumnExists)
	})

	t.Run("no database selected", func(t *testing.T) {
		ctx := NewContext(harness)
		ctx.SetCurrentDatabase("")

		TestQueryWithContext(t, ctx, e, "ALTER TABLE mydb.mytable ADD COLUMN s10 VARCHAR(26)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		tbl, ok, err := db.GetTableInsensitive(NewContext(harness), "mytable")
		require.NoError(err)
		require.True(ok)
		assertSchemasEqualWithDefaults(t, sql.Schema{
			{Name: "s3", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 25), Source: "mytable", Comment: "hello", Nullable: true, Default: parse.MustStringToColumnDefaultValue(NewContext(harness), `"yay"`, sql.MustCreateStringWithDefaults(sqltypes.VarChar, 25), true)},
			{Name: "s4", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 1), Source: "mytable"},
			{Name: "i", Type: sql.Int64, Source: "mytable", PrimaryKey: true},
			{Name: "s2", Type: sql.Text, Source: "mytable", Comment: "hello", Nullable: true},
			{Name: "s", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 20), Source: "mytable", Comment: "column s"},
			{Name: "i2", Type: sql.Int32, Source: "mytable", Comment: "hello", Nullable: true, Default: parse.MustStringToColumnDefaultValue(NewContext(harness), "42", sql.Int32, true)},
			{Name: "s5", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 26), Source: "mytable", Nullable: true},
			{Name: "s6", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 27), Source: "mytable", Nullable: true},
			{Name: "s10", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 26), Source: "mytable", Nullable: true},
		}, tbl.Schema())
	})
}

//todo(max): convert to WriteQueryTest
func TestModifyColumn(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData, setup.Mytable_del_idxData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	db, err := e.Analyzer.Catalog.Database(NewContext(harness), "mydb")
	require.NoError(t, err)

	TestQueryWithContext(t, ctx, e, "ALTER TABLE mytable MODIFY COLUMN i TEXT NOT NULL COMMENT 'modified'", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
	tbl, ok, err := db.GetTableInsensitive(NewContext(harness), "mytable")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, sql.Schema{
		{Name: "i", Type: sql.Text, Source: "mytable", Comment: "modified", PrimaryKey: true},
		{Name: "s", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 20), Source: "mytable", Comment: "column s"},
	}, tbl.Schema())

	TestQueryWithContext(t, ctx, e, "ALTER TABLE mytable MODIFY COLUMN i TINYINT NOT NULL COMMENT 'yes' AFTER s", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

	tbl, ok, err = db.GetTableInsensitive(NewContext(harness), "mytable")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, sql.Schema{
		{Name: "s", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 20), Source: "mytable", Comment: "column s"},
		{Name: "i", Type: sql.Int8, Source: "mytable", Comment: "yes", PrimaryKey: true},
	}, tbl.Schema())

	TestQueryWithContext(t, ctx, e, "ALTER TABLE mytable MODIFY COLUMN i BIGINT NOT NULL COMMENT 'ok' FIRST", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

	tbl, ok, err = db.GetTableInsensitive(NewContext(harness), "mytable")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, sql.Schema{
		{Name: "i", Type: sql.Int64, Source: "mytable", Comment: "ok", PrimaryKey: true},
		{Name: "s", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 20), Source: "mytable", Comment: "column s"},
	}, tbl.Schema())

	TestQueryWithContext(t, ctx, e, "ALTER TABLE mytable MODIFY COLUMN s VARCHAR(20) NULL COMMENT 'changed'", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

	tbl, ok, err = db.GetTableInsensitive(NewContext(harness), "mytable")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, sql.Schema{
		{Name: "i", Type: sql.Int64, Source: "mytable", Comment: "ok", PrimaryKey: true},
		{Name: "s", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 20), Nullable: true, Source: "mytable", Comment: "changed"},
	}, tbl.Schema())

	AssertErr(t, e, harness, "ALTER TABLE mytable MODIFY not_exist BIGINT NOT NULL COMMENT 'ok' FIRST", sql.ErrTableColumnNotFound)
	AssertErr(t, e, harness, "ALTER TABLE mytable MODIFY i BIGINT NOT NULL COMMENT 'ok' AFTER not_exist", sql.ErrTableColumnNotFound)
	AssertErr(t, e, harness, "ALTER TABLE not_exist MODIFY COLUMN i INT NOT NULL COMMENT 'hello'", sql.ErrTableNotFound)

	t.Run("auto increment attribute", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "ALTER TABLE mytable MODIFY i BIGINT auto_increment", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		tbl, ok, err := db.GetTableInsensitive(NewContext(harness), "mytable")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, sql.Schema{
			{Name: "i", Type: sql.Int64, Source: "mytable", PrimaryKey: true, AutoIncrement: true, Nullable: false, Extra: "auto_increment"},
			{Name: "s", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 20), Nullable: true, Source: "mytable", Comment: "changed"},
		}, tbl.Schema())

		RunQuery(t, e, harness, "insert into mytable (s) values ('new row')")
		TestQueryWithContext(t, ctx, e, "select i from mytable where s = 'new row'", []sql.Row{{4}}, nil, nil)

		AssertErr(t, e, harness, "ALTER TABLE mytable add column i2 bigint auto_increment", sql.ErrInvalidAutoIncCols)

		RunQuery(t, e, harness, "alter table mytable add column i2 bigint")
		AssertErr(t, e, harness, "ALTER TABLE mytable modify column i2 bigint auto_increment", sql.ErrInvalidAutoIncCols)

		tbl, ok, err = db.GetTableInsensitive(NewContext(harness), "mytable")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, sql.Schema{
			{Name: "i", Type: sql.Int64, Source: "mytable", PrimaryKey: true, AutoIncrement: true, Extra: "auto_increment"},
			{Name: "s", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 20), Nullable: true, Source: "mytable", Comment: "changed"},
			{Name: "i2", Type: sql.Int64, Source: "mytable", Nullable: true},
		}, tbl.Schema())
	})

	t.Run("no database selected", func(t *testing.T) {
		ctx := NewContext(harness)
		ctx.SetCurrentDatabase("")

		TestQueryWithContext(t, ctx, e, "ALTER TABLE mydb.mytable MODIFY COLUMN s VARCHAR(21) NULL COMMENT 'changed again'", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		tbl, ok, err = db.GetTableInsensitive(NewContext(harness), "mytable")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, sql.Schema{
			{Name: "i", Type: sql.Int64, Source: "mytable", PrimaryKey: true, AutoIncrement: true, Extra: "auto_increment"},
			{Name: "s", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 21), Nullable: true, Source: "mytable", Comment: "changed again"},
			{Name: "i2", Type: sql.Int64, Source: "mytable", Nullable: true},
		}, tbl.Schema())
	})
}

// todo(max): convert to WriteQueryTest
func TestDropColumn(t *testing.T, harness Harness) {
	require := require.New(t)

	harness.Setup(setup.MydbData, setup.MytableData, setup.TabletestData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)
	db, err := e.Analyzer.Catalog.Database(ctx, "mydb")
	require.NoError(err)

	t.Run("drop last column", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "ALTER TABLE mytable DROP COLUMN s", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		tbl, ok, err := db.GetTableInsensitive(ctx, "mytable")
		require.NoError(err)
		require.True(ok)
		assert.Equal(t, sql.Schema{
			{Name: "i", Type: sql.Int64, Source: "mytable", PrimaryKey: true},
		}, tbl.Schema())

		TestQueryWithContext(t, ctx, e, "select * from mytable order by i", []sql.Row{
			{1}, {2}, {3},
		}, nil, nil)
	})

	t.Run("drop first column", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t1 (a int, b varchar(10), c bigint, k bigint primary key)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "insert into t1 values (1, 'abc', 2, 3), (4, 'def', 5, 6)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t1 DROP COLUMN a", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		tbl, ok, err := db.GetTableInsensitive(ctx, "t1")
		require.NoError(err)
		require.True(ok)
		assert.Equal(t, sql.Schema{
			{Name: "b", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 10), Source: "t1", Nullable: true},
			{Name: "c", Type: sql.Int64, Source: "t1", Nullable: true},
			{Name: "k", Type: sql.Int64, Source: "t1", PrimaryKey: true},
		}, tbl.Schema())

		TestQueryWithContext(t, ctx, e, "select * from t1 order by b", []sql.Row{
			{"abc", 2, 3},
			{"def", 5, 6},
		}, nil, nil)
	})

	t.Run("drop middle column", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t2 (a int, b varchar(10), c bigint, k bigint primary key)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "insert into t2 values (1, 'abc', 2, 3), (4, 'def', 5, 6)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t2 DROP COLUMN b", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		tbl, ok, err := db.GetTableInsensitive(ctx, "t2")
		require.NoError(err)
		require.True(ok)
		assert.Equal(t, sql.Schema{
			{Name: "a", Type: sql.Int32, Source: "t2", Nullable: true},
			{Name: "c", Type: sql.Int64, Source: "t2", Nullable: true},
			{Name: "k", Type: sql.Int64, Source: "t2", PrimaryKey: true},
		}, tbl.Schema())

		TestQueryWithContext(t, ctx, e, "select * from t2 order by c", []sql.Row{
			{1, 2, 3},
			{4, 5, 6},
		}, nil, nil)
	})

	t.Run("drop primary key column", func(t *testing.T) {
		t.Skip("primary key column drops not well supported yet")

		TestQueryWithContext(t, ctx, e, "CREATE TABLE t3 (a int primary key, b varchar(10), c bigint)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "insert into t3 values (1, 'abc', 2), (3, 'def', 4)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t3 DROP COLUMN a", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		tbl, ok, err := db.GetTableInsensitive(ctx, "t1")
		require.NoError(err)
		require.True(ok)
		assert.Equal(t, sql.Schema{
			{Name: "b", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 10), Source: "t3", Nullable: true},
			{Name: "c", Type: sql.Int64, Source: "t3", Nullable: true},
		}, tbl.Schema())

		TestQueryWithContext(t, ctx, e, "select * from t3 order by b", []sql.Row{
			{"abc", 2, 3},
			{"def", 4, 5},
		}, nil, nil)
	})

	t.Run("no database selected", func(t *testing.T) {
		ctx := NewContext(harness)
		ctx.SetCurrentDatabase("")

		beforeDropTbl, _, _ := db.GetTableInsensitive(NewContext(harness), "tabletest")

		TestQueryWithContext(t, ctx, e, "ALTER TABLE mydb.tabletest DROP COLUMN s", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		tbl, ok, err := db.GetTableInsensitive(NewContext(harness), "tabletest")
		require.NoError(err)
		require.True(ok)
		assert.NotEqual(t, beforeDropTbl, tbl.Schema())
		assert.Equal(t, sql.Schema{
			{Name: "i", Type: sql.Int32, Source: "tabletest", PrimaryKey: true},
		}, tbl.Schema())
	})

	t.Run("error cases", func(t *testing.T) {
		AssertErr(t, e, harness, "ALTER TABLE not_exist DROP COLUMN s", sql.ErrTableNotFound)
		AssertErr(t, e, harness, "ALTER TABLE mytable DROP COLUMN s", sql.ErrTableColumnNotFound)

		// Dropping a column referred to in another column's default
		RunQuery(t, e, harness, "create table t3 (a int primary key, b int, c int default (b+10))")
		AssertErr(t, e, harness, "ALTER TABLE t3 DROP COLUMN b", sql.ErrDropColumnReferencedInDefault)
	})
}

func TestDropColumnKeylessTables(t *testing.T, harness Harness) {
	require := require.New(t)

	harness.Setup(setup.MydbData, setup.TabletestData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)
	db, err := e.Analyzer.Catalog.Database(ctx, "mydb")
	require.NoError(err)

	t.Run("drop last column", func(t *testing.T) {
		RunQuery(t, e, harness, "create table t0 (i bigint, s varchar(20))")

		TestQueryWithContext(t, ctx, e, "ALTER TABLE t0 DROP COLUMN s", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		tbl, ok, err := db.GetTableInsensitive(ctx, "t0")
		require.NoError(err)
		require.True(ok)
		assert.Equal(t, sql.Schema{
			{Name: "i", Type: sql.Int64, Source: "t0", Nullable: true},
		}, tbl.Schema())
	})

	t.Run("drop first column", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t1 (a int, b varchar(10), c bigint)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "insert into t1 values (1, 'abc', 2), (4, 'def', 5)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t1 DROP COLUMN a", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		tbl, ok, err := db.GetTableInsensitive(ctx, "t1")
		require.NoError(err)
		require.True(ok)
		assert.Equal(t, sql.Schema{
			{Name: "b", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 10), Source: "t1", Nullable: true},
			{Name: "c", Type: sql.Int64, Source: "t1", Nullable: true},
		}, tbl.Schema())

		TestQueryWithContext(t, ctx, e, "select * from t1 order by b", []sql.Row{
			{"abc", 2},
			{"def", 5},
		}, nil, nil)
	})

	t.Run("drop middle column", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t2 (a int, b varchar(10), c bigint)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "insert into t2 values (1, 'abc', 2), (4, 'def', 5)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t2 DROP COLUMN b", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		tbl, ok, err := db.GetTableInsensitive(ctx, "t2")
		require.NoError(err)
		require.True(ok)
		assert.Equal(t, sql.Schema{
			{Name: "a", Type: sql.Int32, Source: "t2", Nullable: true},
			{Name: "c", Type: sql.Int64, Source: "t2", Nullable: true},
		}, tbl.Schema())

		TestQueryWithContext(t, ctx, e, "select * from t2 order by c", []sql.Row{
			{1, 2},
			{4, 5},
		}, nil, nil)
	})

	t.Run("no database selected", func(t *testing.T) {
		ctx := NewContext(harness)
		ctx.SetCurrentDatabase("")

		beforeDropTbl, _, _ := db.GetTableInsensitive(NewContext(harness), "tabletest")

		TestQueryWithContext(t, ctx, e, "ALTER TABLE mydb.tabletest DROP COLUMN s", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		tbl, ok, err := db.GetTableInsensitive(NewContext(harness), "tabletest")
		require.NoError(err)
		require.True(ok)
		assert.NotEqual(t, beforeDropTbl, tbl.Schema())
		assert.Equal(t, sql.Schema{
			{Name: "i", Type: sql.Int32, Source: "tabletest", PrimaryKey: true},
		}, tbl.Schema())
	})

	t.Run("error cases", func(t *testing.T) {
		AssertErr(t, e, harness, "ALTER TABLE not_exist DROP COLUMN s", sql.ErrTableNotFound)
		AssertErr(t, e, harness, "ALTER TABLE t0 DROP COLUMN s", sql.ErrTableColumnNotFound)
	})
}

func TestCreateDatabase(t *testing.T, harness Harness) {
	harness.Setup()
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	t.Run("CREATE DATABASE and create table", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE DATABASE testdb", []sql.Row{{sql.OkResult{RowsAffected: 1}}}, nil, nil)

		db, err := e.Analyzer.Catalog.Database(ctx, "testdb")
		require.NoError(t, err)

		TestQueryWithContext(t, ctx, e, "USE testdb", []sql.Row(nil), nil, nil)

		require.Equal(t, ctx.GetCurrentDatabase(), "testdb")

		ctx = NewContext(harness)
		TestQueryWithContext(t, ctx, e, "CREATE TABLE test (pk int primary key)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		db, err = e.Analyzer.Catalog.Database(ctx, "testdb")
		require.NoError(t, err)

		_, ok, err := db.GetTableInsensitive(ctx, "test")

		require.NoError(t, err)
		require.True(t, ok)
	})

	t.Run("CREATE DATABASE IF NOT EXISTS", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE DATABASE IF NOT EXISTS testdb2", []sql.Row{{sql.OkResult{RowsAffected: 1}}}, nil, nil)

		db, err := e.Analyzer.Catalog.Database(ctx, "testdb2")
		require.NoError(t, err)

		TestQueryWithContext(t, ctx, e, "USE testdb2", []sql.Row(nil), nil, nil)

		require.Equal(t, ctx.GetCurrentDatabase(), "testdb2")

		ctx = NewContext(harness)
		TestQueryWithContext(t, ctx, e, "CREATE TABLE test (pk int primary key)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		db, err = e.Analyzer.Catalog.Database(ctx, "testdb2")
		require.NoError(t, err)

		_, ok, err := db.GetTableInsensitive(ctx, "test")

		require.NoError(t, err)
		require.True(t, ok)
	})

	t.Run("CREATE SCHEMA", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE SCHEMA testdb3", []sql.Row{{sql.OkResult{RowsAffected: 1}}}, nil, nil)

		db, err := e.Analyzer.Catalog.Database(ctx, "testdb3")
		require.NoError(t, err)

		TestQueryWithContext(t, ctx, e, "USE testdb3", []sql.Row(nil), nil, nil)

		require.Equal(t, ctx.GetCurrentDatabase(), "testdb3")

		ctx = NewContext(harness)
		TestQueryWithContext(t, ctx, e, "CREATE TABLE test (pk int primary key)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		db, err = e.Analyzer.Catalog.Database(ctx, "testdb3")
		require.NoError(t, err)

		_, ok, err := db.GetTableInsensitive(ctx, "test")

		require.NoError(t, err)
		require.True(t, ok)
	})

	t.Run("CREATE DATABASE error handling", func(t *testing.T) {
		AssertWarningAndTestQuery(t, e, ctx, harness, "CREATE DATABASE newtestdb CHARACTER SET utf8mb4 ENCRYPTION='N'",
			[]sql.Row{sql.Row{sql.OkResult{RowsAffected: 1, InsertID: 0, Info: nil}}}, nil, mysql.ERNotSupportedYet, 1,
			"", false)

		AssertWarningAndTestQuery(t, e, ctx, harness, "CREATE DATABASE newtest1db DEFAULT COLLATE binary ENCRYPTION='Y'",
			[]sql.Row{sql.Row{sql.OkResult{RowsAffected: 1, InsertID: 0, Info: nil}}}, nil, mysql.ERNotSupportedYet, 1,
			"", false)

		AssertErr(t, e, harness, "CREATE DATABASE mydb", sql.ErrDatabaseExists)

		AssertWarningAndTestQuery(t, e, nil, harness, "CREATE DATABASE IF NOT EXISTS mydb",
			[]sql.Row{{sql.OkResult{RowsAffected: 1}}}, nil, mysql.ERDbCreateExists,
			-1, "", false)
	})
}

func TestPkOrdinalsDDL(t *testing.T, harness Harness) {
	harness.Setup(setup.OrdinalSetup...)
	for _, tt := range queries.OrdinalDDLQueries {
		TestQuery(t, harness, tt.Query, tt.Expected, tt.ExpectedColumns, nil)
	}

	for _, tt := range queries.OrdinalDDLWriteQueries {
		runWriteQueryTest(t, harness, tt)
	}
}

func TestPkOrdinalsDML(t *testing.T, harness Harness) {
	dml := []struct {
		create string
		insert string
		mutate string
		sel    string
		exp    []sql.Row
	}{
		{
			create: "CREATE TABLE a (x int, y int, z int, w int, primary key (z,x))",
			insert: "INSERT INTO a values (0,0,0,0), (1,1,1,1), (2,2,2,2)",
			mutate: "DELETE FROM a WHERE x = 0",
			sel:    "select * from a",
			exp:    []sql.Row{{1, 1, 1, 1}, {2, 2, 2, 2}},
		},
		{
			create: "CREATE TABLE a (x int, y int, z int, w int, primary key (z,x,w))",
			insert: "INSERT INTO a values (0,0,0,0), (1,1,1,1), (2,2,2,2)",
			mutate: "DELETE FROM a WHERE x = 0 and z = 0",
			sel:    "select * from a",
			exp:    []sql.Row{{1, 1, 1, 1}, {2, 2, 2, 2}},
		},
		{
			create: "CREATE TABLE a (x int, y int, z int, w int, primary key (z,x))",
			insert: "INSERT INTO a values (0,NULL,0,0), (1,NULL,1,1), (2,2,2,2)",
			mutate: "DELETE FROM a WHERE y = 2",
			sel:    "select * from a",
			exp:    []sql.Row{{0, nil, 0, 0}, {1, nil, 1, 1}},
		},
		{
			create: "CREATE TABLE a (x int, y int, z int, w int, primary key (z,x))",
			insert: "INSERT INTO a values (0,NULL,0,0), (1,NULL,1,1), (2,2,2,2)",
			mutate: "DELETE FROM a WHERE y in (2)",
			sel:    "select * from a",
			exp:    []sql.Row{{0, nil, 0, 0}, {1, nil, 1, 1}},
		},
		{
			create: "CREATE TABLE a (x int, y int, z int, w int, primary key (z,x))",
			insert: "INSERT INTO a values (0,NULL,0,0), (1,NULL,1,1), (2,2,2,2)",
			mutate: "DELETE FROM a WHERE y not in (NULL)",
			sel:    "select * from a",
			exp:    []sql.Row{{0, nil, 0, 0}, {1, nil, 1, 1}, {2, 2, 2, 2}},
		},
		{
			create: "CREATE TABLE a (x int, y int, z int, w int, primary key (z,x))",
			insert: "INSERT INTO a values (0,NULL,0,0), (1,NULL,1,1), (2,2,2,2)",
			mutate: "DELETE FROM a WHERE y IS NOT NULL",
			sel:    "select * from a",
			exp:    []sql.Row{{0, nil, 0, 0}, {1, nil, 1, 1}},
		},
		{
			create: "CREATE TABLE a (x int, y int, z int, w int, primary key (z,x))",
			insert: "INSERT INTO a values (0,NULL,0,0), (1,NULL,1,1), (2,2,2,2)",
			mutate: "DELETE FROM a WHERE y IS NULL",
			sel:    "select * from a",
			exp:    []sql.Row{{2, 2, 2, 2}},
		},
		{
			create: "CREATE TABLE a (x int, y int, z int, w int, primary key (z,x))",
			insert: "INSERT INTO a values (0,NULL,0,0), (1,NULL,1,1), (2,2,2,2)",
			mutate: "DELETE FROM a WHERE y = NULL",
			sel:    "select * from a",
			exp:    []sql.Row{{0, nil, 0, 0}, {1, nil, 1, 1}, {2, 2, 2, 2}},
		},
		{
			create: "CREATE TABLE a (x int, y int, z int, w int, primary key (z,x))",
			insert: "INSERT INTO a values (0,NULL,0,0), (1,NULL,1,1), (2,2,2,2)",
			mutate: "DELETE FROM a WHERE y = NULL or y in (2,4)",
			sel:    "select * from a",
			exp:    []sql.Row{{0, nil, 0, 0}, {1, nil, 1, 1}},
		},
		{
			create: "CREATE TABLE a (x int, y int, z int, w int, primary key (z,x))",
			insert: "INSERT INTO a values (0,NULL,0,0), (1,NULL,1,1), (2,2,2,2)",
			mutate: "DELETE FROM a WHERE y IS NULL or y in (2,4)",
			sel:    "select * from a",
			exp:    []sql.Row{},
		},
		{
			create: "CREATE TABLE a (x int, y int, z int, w int, primary key (z,x))",
			insert: "INSERT INTO a values (0,NULL,0,0), (1,NULL,1,1), (2,2,2,2)",
			mutate: "DELETE FROM a WHERE y IS NULL AND z != 0",
			sel:    "select * from a",
			exp:    []sql.Row{{0, nil, 0, 0}, {2, 2, 2, 2}},
		},
		{
			create: "CREATE TABLE a (x int, y int, z int, w int, primary key (z,x))",
			insert: "INSERT INTO a values (0,NULL,0,0), (1,NULL,1,1), (2,2,2,2)",
			mutate: "DELETE FROM a WHERE y != NULL",
			sel:    "select * from a",
			exp:    []sql.Row{{0, nil, 0, 0}, {1, nil, 1, 1}, {2, 2, 2, 2}},
		},
		{
			create: "CREATE TABLE a (x int, y int, z int, w int, primary key (z,x,w))",
			insert: "INSERT INTO a values (0,NULL,0,0), (1,NULL,1,1), (2,2,2,2)",
			mutate: "DELETE FROM a WHERE x in (0,2) and z in (0,4)",
			sel:    "select * from a",
			exp:    []sql.Row{{1, nil, 1, 1}, {2, 2, 2, 2}},
		},
		{
			create: "CREATE TABLE a (x int, y int, z int, w int, primary key (z,x))",
			insert: "INSERT INTO a values (0,NULL,0,0), (1,NULL,1,1), (2,2,2,2)",
			mutate: "DELETE FROM a WHERE y in (2,-1)",
			sel:    "select * from a",
			exp:    []sql.Row{{0, nil, 0, 0}, {1, nil, 1, 1}},
		},
		{
			create: "CREATE TABLE a (x int, y int, z int, w int, primary key (z,x))",
			insert: "INSERT INTO a values (0,NULL,0,0), (1,NULL,1,1), (2,2,2,2)",
			mutate: "DELETE FROM a WHERE y < 3",
			sel:    "select * from a",
			exp:    []sql.Row{{0, nil, 0, 0}, {1, nil, 1, 1}},
		},
		{
			create: "CREATE TABLE a (x int, y int, z int, w int, primary key (z,x))",
			insert: "INSERT INTO a values (0,NULL,0,0), (1,NULL,1,1), (2,2,2,2)",
			mutate: "DELETE FROM a WHERE y > 0 and z = 2",
			sel:    "select * from a",
			exp:    []sql.Row{{0, nil, 0, 0}, {1, nil, 1, 1}},
		},
		{
			create: "CREATE TABLE a (x int, y int, z int, w int, primary key (z,x))",
			insert: "INSERT INTO a values (0,NULL,0,0), (1,NULL,1,1), (2,2,2,2)",
			mutate: "DELETE FROM a WHERE y = 2",
			sel:    "select y from a",
			exp:    []sql.Row{{nil}, {nil}},
		},
		{
			create: "CREATE TABLE a (x int, y int, z int, w int, index idx1 (y))",
			insert: "INSERT INTO a values (0,0,0,0), (1,1,1,1), (2,2,2,2)",
			mutate: "",
			sel:    "select * from a where y = 3",
			exp:    []sql.Row{},
		},
	}

	harness.Setup(setup.MydbData, setup.MytableData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)
	RunQuery(t, e, harness, "create table b (y char(6) primary key)")
	RunQuery(t, e, harness, "insert into b values ('aaaaaa'),('bbbbbb'),('cccccc')")
	for _, tt := range dml {
		t.Run(fmt.Sprintf("%s", tt.mutate), func(t *testing.T) {
			defer RunQuery(t, e, harness, "DROP TABLE IF EXISTS a")
			if tt.create != "" {
				RunQuery(t, e, harness, tt.create)
			}
			if tt.insert != "" {
				RunQuery(t, e, harness, tt.insert)
			}
			if tt.mutate != "" {
				RunQuery(t, e, harness, tt.mutate)
			}
			TestQueryWithContext(t, ctx, e, tt.sel, tt.exp, nil, nil)
		})
	}
}

func TestDropDatabase(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)
	t.Run("DROP DATABASE correctly works", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "DROP DATABASE mydb", []sql.Row{{sql.OkResult{RowsAffected: 1}}}, nil, nil)

		_, err := e.Analyzer.Catalog.Database(NewContext(harness), "mydb")
		require.Error(t, err)

		// TODO: Deal with handling this error.
		//AssertErr(t, e, harness, "SHOW TABLES", sql.ErrNoDatabaseSelected)
	})

	t.Run("DROP DATABASE works on newly created databases.", func(t *testing.T) {
		e := mustNewEngine(t, harness)
		defer e.Close()
		TestQueryWithContext(t, ctx, e, "CREATE DATABASE testdb", []sql.Row{{sql.OkResult{RowsAffected: 1}}}, nil, nil)

		_, err := e.Analyzer.Catalog.Database(NewContext(harness), "testdb")
		require.NoError(t, err)

		TestQueryWithContext(t, ctx, e, "DROP DATABASE testdb", []sql.Row{{sql.OkResult{RowsAffected: 1}}}, nil, nil)
		AssertErr(t, e, harness, "USE testdb", sql.ErrDatabaseNotFound)
	})

	t.Run("DROP DATABASE works on current database and sets current database to empty.", func(t *testing.T) {
		e := mustNewEngine(t, harness)
		defer e.Close()
		TestQueryWithContext(t, ctx, e, "CREATE DATABASE testdb", []sql.Row{{sql.OkResult{RowsAffected: 1}}}, nil, nil)
		RunQueryWithContext(t, e, ctx, "USE TESTdb")

		_, err := e.Analyzer.Catalog.Database(NewContext(harness), "testdb")
		require.NoError(t, err)

		TestQueryWithContext(t, ctx, e, "DROP DATABASE TESTDB", []sql.Row{{sql.OkResult{RowsAffected: 1}}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT DATABASE()", []sql.Row{{nil}}, nil, nil)
		AssertErr(t, e, harness, "USE testdb", sql.ErrDatabaseNotFound)
	})

	t.Run("DROP SCHEMA works on newly created databases.", func(t *testing.T) {
		e := mustNewEngine(t, harness)
		defer e.Close()
		TestQueryWithContext(t, ctx, e, "CREATE SCHEMA testdb", []sql.Row{{sql.OkResult{RowsAffected: 1}}}, nil, nil)

		_, err := e.Analyzer.Catalog.Database(NewContext(harness), "testdb")
		require.NoError(t, err)

		TestQueryWithContext(t, ctx, e, "DROP SCHEMA testdb", []sql.Row{{sql.OkResult{RowsAffected: 1}}}, nil, nil)

		AssertErr(t, e, harness, "USE testdb", sql.ErrDatabaseNotFound)
	})

	t.Run("DROP DATABASE IF EXISTS correctly works.", func(t *testing.T) {
		e := mustNewEngine(t, harness)
		defer e.Close()

		// The test setup sets a database name, which interferes with DROP DATABASE tests
		ctx := NewContext(harness)
		TestQueryWithContext(t, ctx, e, "DROP DATABASE mydb", []sql.Row{{sql.OkResult{RowsAffected: 1}}}, nil, nil)
		AssertWarningAndTestQuery(t, e, ctx, harness, "DROP DATABASE IF EXISTS mydb",
			[]sql.Row{{sql.OkResult{RowsAffected: 0}}}, nil, mysql.ERDbDropExists,
			-1, "", false)

		TestQueryWithContext(t, ctx, e, "CREATE DATABASE testdb", []sql.Row{{sql.OkResult{RowsAffected: 1}}}, nil, nil)

		_, err := e.Analyzer.Catalog.Database(ctx, "testdb")
		require.NoError(t, err)

		TestQueryWithContext(t, ctx, e, "DROP DATABASE IF EXISTS testdb", []sql.Row{{sql.OkResult{RowsAffected: 1}}}, nil, nil)

		sch, iter, err := e.Query(ctx, "USE testdb")
		if err == nil {
			_, err = sql.RowIterToRows(ctx, sch, iter)
		}
		require.Error(t, err)
		require.True(t, sql.ErrDatabaseNotFound.Is(err), "Expected error of type %s but got %s", sql.ErrDatabaseNotFound, err)

		AssertWarningAndTestQuery(t, e, ctx, harness, "DROP DATABASE IF EXISTS testdb",
			[]sql.Row{{sql.OkResult{RowsAffected: 0}}}, nil, mysql.ERDbDropExists,
			-1, "", false)
	})
}

func TestCreateForeignKeys(t *testing.T, harness Harness) {
	require := require.New(t)

	harness.Setup(setup.MydbData, setup.MytableData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)
	TestQueryWithContext(t, ctx, e, "CREATE TABLE parent(a INTEGER PRIMARY KEY, b INTEGER)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, "ALTER TABLE parent ADD INDEX pb (b)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, "CREATE TABLE child(c INTEGER PRIMARY KEY, d INTEGER, "+
		"CONSTRAINT fk1 FOREIGN KEY (D) REFERENCES parent(B) ON DELETE CASCADE"+
		")", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, "ALTER TABLE child ADD CONSTRAINT fk4 FOREIGN KEY (D) REFERENCES child(C)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

	db, err := e.Analyzer.Catalog.Database(ctx, "mydb")
	require.NoError(err)

	child, ok, err := db.GetTableInsensitive(ctx, "child")
	require.NoError(err)
	require.True(ok)

	fkt, ok := child.(sql.ForeignKeyTable)
	require.True(ok)

	fks, err := fkt.GetDeclaredForeignKeys(NewContext(harness))
	require.NoError(err)

	expected := []sql.ForeignKeyConstraint{
		{
			Name:           "fk1",
			Database:       "mydb",
			Table:          "child",
			Columns:        []string{"d"},
			ParentDatabase: "mydb",
			ParentTable:    "parent",
			ParentColumns:  []string{"b"},
			OnUpdate:       sql.ForeignKeyReferentialAction_DefaultAction,
			OnDelete:       sql.ForeignKeyReferentialAction_Cascade,
			IsResolved:     true,
		},
		{
			Name:           "fk4",
			Database:       "mydb",
			Table:          "child",
			Columns:        []string{"d"},
			ParentDatabase: "mydb",
			ParentTable:    "child",
			ParentColumns:  []string{"c"},
			OnUpdate:       sql.ForeignKeyReferentialAction_DefaultAction,
			OnDelete:       sql.ForeignKeyReferentialAction_DefaultAction,
			IsResolved:     true,
		},
	}
	assert.Equal(t, expected, fks)

	TestQueryWithContext(t, ctx, e, "CREATE TABLE child2(e INTEGER PRIMARY KEY, f INTEGER)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, "ALTER TABLE child2 ADD CONSTRAINT fk2 FOREIGN KEY (f) REFERENCES parent(b) ON DELETE RESTRICT", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, "ALTER TABLE child2 ADD CONSTRAINT fk3 FOREIGN KEY (f) REFERENCES child(d) ON UPDATE SET NULL", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

	child, ok, err = db.GetTableInsensitive(ctx, "child2")
	require.NoError(err)
	require.True(ok)

	fkt, ok = child.(sql.ForeignKeyTable)
	require.True(ok)

	fks, err = fkt.GetDeclaredForeignKeys(NewContext(harness))
	require.NoError(err)

	expected = []sql.ForeignKeyConstraint{
		{
			Name:           "fk2",
			Database:       "mydb",
			Table:          "child2",
			Columns:        []string{"f"},
			ParentDatabase: "mydb",
			ParentTable:    "parent",
			ParentColumns:  []string{"b"},
			OnUpdate:       sql.ForeignKeyReferentialAction_DefaultAction,
			OnDelete:       sql.ForeignKeyReferentialAction_Restrict,
			IsResolved:     true,
		},
		{
			Name:           "fk3",
			Database:       "mydb",
			Table:          "child2",
			Columns:        []string{"f"},
			ParentDatabase: "mydb",
			ParentTable:    "child",
			ParentColumns:  []string{"d"},
			OnUpdate:       sql.ForeignKeyReferentialAction_SetNull,
			OnDelete:       sql.ForeignKeyReferentialAction_DefaultAction,
			IsResolved:     true,
		},
	}
	assert.Equal(t, expected, fks)

	// Some faulty create statements
	_, _, err = e.Query(NewContext(harness), "ALTER TABLE child2 ADD CONSTRAINT fk3 FOREIGN KEY (f) REFERENCES dne(d) ON UPDATE SET NULL")
	require.Error(err)

	_, _, err = e.Query(NewContext(harness), "ALTER TABLE child2 ADD CONSTRAINT fk4 FOREIGN KEY (f) REFERENCES dne(d) ON UPDATE SET NULL")
	require.Error(err)
	assert.True(t, sql.ErrTableNotFound.Is(err))

	_, _, err = e.Query(NewContext(harness), "ALTER TABLE dne ADD CONSTRAINT fk4 FOREIGN KEY (f) REFERENCES child(d) ON UPDATE SET NULL")
	require.Error(err)
	assert.True(t, sql.ErrTableNotFound.Is(err))

	_, _, err = e.Query(NewContext(harness), "ALTER TABLE child2 ADD CONSTRAINT fk5 FOREIGN KEY (f) REFERENCES child(dne) ON UPDATE SET NULL")
	require.Error(err)
	assert.True(t, sql.ErrTableColumnNotFound.Is(err))

	t.Run("Add a column then immediately add a foreign key", func(t *testing.T) {
		RunQuery(t, e, harness, "CREATE TABLE parent3 (pk BIGINT PRIMARY KEY, v1 BIGINT, INDEX (v1))")
		RunQuery(t, e, harness, "CREATE TABLE child3 (pk BIGINT PRIMARY KEY);")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE child3 ADD COLUMN v1 BIGINT NULL, ADD CONSTRAINT fk_child3 FOREIGN KEY (v1) REFERENCES parent3(v1);", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
	})

	TestScript(t, harness, queries.ScriptTest{
		Name: "Do not validate foreign keys if FOREIGN_KEY_CHECKS is set to zero",
		Assertions: []queries.ScriptTestAssertion{
			{
				Query:    "SET FOREIGN_KEY_CHECKS=0;",
				Expected: []sql.Row{{}},
			},
			{
				Query:    "CREATE TABLE child4 (pk BIGINT PRIMARY KEY, CONSTRAINT fk_child4 FOREIGN KEY (pk) REFERENCES delayed_parent4 (pk))",
				Expected: []sql.Row{{sql.NewOkResult(0)}},
			},
			{
				Query:    "CREATE TABLE delayed_parent4 (pk BIGINT PRIMARY KEY)",
				Expected: []sql.Row{{sql.NewOkResult(0)}},
			},
		},
	})
}

func TestDropForeignKeys(t *testing.T, harness Harness) {
	require := require.New(t)

	harness.Setup(setup.MydbData, setup.MytableData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	TestQueryWithContext(t, ctx, e, "CREATE TABLE parent(a INTEGER PRIMARY KEY, b INTEGER)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, "ALTER TABLE parent ADD INDEX pb (b)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, "CREATE TABLE child(c INTEGER PRIMARY KEY, d INTEGER, "+
		"CONSTRAINT fk1 FOREIGN KEY (d) REFERENCES parent(b) ON DELETE CASCADE"+
		")", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

	TestQueryWithContext(t, ctx, e, "CREATE TABLE child2(e INTEGER PRIMARY KEY, f INTEGER)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, "ALTER TABLE child2 ADD CONSTRAINT fk2 FOREIGN KEY (f) REFERENCES parent(b) ON DELETE RESTRICT, "+
		"ADD CONSTRAINT fk3 FOREIGN KEY (f) REFERENCES child(d) ON UPDATE SET NULL", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, "ALTER TABLE child2 DROP CONSTRAINT fk2", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

	db, err := e.Analyzer.Catalog.Database(NewContext(harness), "mydb")
	require.NoError(err)

	child, ok, err := db.GetTableInsensitive(NewContext(harness), "child2")
	require.NoError(err)
	require.True(ok)

	fkt, ok := child.(sql.ForeignKeyTable)
	require.True(ok)

	fks, err := fkt.GetDeclaredForeignKeys(NewContext(harness))
	require.NoError(err)

	expected := []sql.ForeignKeyConstraint{
		{
			Name:           "fk3",
			Database:       "mydb",
			Table:          "child2",
			Columns:        []string{"f"},
			ParentDatabase: "mydb",
			ParentTable:    "child",
			ParentColumns:  []string{"d"},
			OnUpdate:       sql.ForeignKeyReferentialAction_SetNull,
			OnDelete:       sql.ForeignKeyReferentialAction_DefaultAction,
			IsResolved:     true,
		},
	}
	assert.Equal(t, expected, fks)

	TestQueryWithContext(t, ctx, e, "ALTER TABLE child2 DROP FOREIGN KEY fk3", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

	child, ok, err = db.GetTableInsensitive(NewContext(harness), "child2")
	require.NoError(err)
	require.True(ok)

	fkt, ok = child.(sql.ForeignKeyTable)
	require.True(ok)

	fks, err = fkt.GetDeclaredForeignKeys(NewContext(harness))
	require.NoError(err)
	assert.Len(t, fks, 0)

	// Some error queries
	AssertErr(t, e, harness, "ALTER TABLE child3 DROP CONSTRAINT dne", sql.ErrTableNotFound)
	AssertErr(t, e, harness, "ALTER TABLE child2 DROP CONSTRAINT fk3", sql.ErrUnknownConstraint)
	AssertErr(t, e, harness, "ALTER TABLE child2 DROP FOREIGN KEY fk3", sql.ErrForeignKeyNotFound)
}

func TestForeignKeys(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.Parent_childData)
	for _, script := range queries.ForeignKeyTests {
		TestScript(t, harness, script)
	}
}

// todo(max): rewrite this using info schema and []QueryTest
func TestCreateCheckConstraints(t *testing.T, harness Harness) {
	require := require.New(t)

	harness.Setup(setup.ChecksSetup...)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	db, err := e.Analyzer.Catalog.Database(NewContext(harness), "mydb")
	require.NoError(err)

	table, ok, err := db.GetTableInsensitive(ctx, "checks")
	require.NoError(err)
	require.True(ok)

	cht, ok := table.(sql.CheckTable)
	require.True(ok)

	checks, err := cht.GetChecks(NewContext(harness))
	require.NoError(err)

	expected := []sql.CheckDefinition{
		{
			Name:            "chk1",
			CheckExpression: "(b > 0)",
			Enforced:        true,
		},
		{
			Name:            "chk2",
			CheckExpression: "(b > 0)",
			Enforced:        false,
		},
		{
			Name:            "chk3",
			CheckExpression: "(b > 1)",
			Enforced:        true,
		},
		{
			Name:            "chk4",
			CheckExpression: "(UPPER(c) = c)",
			Enforced:        true,
		},
	}
	assert.Equal(t, expected, checks)

	// Unnamed constraint
	RunQuery(t, e, harness, "ALTER TABLE checks ADD CONSTRAINT CHECK (b > 100)")

	table, ok, err = db.GetTableInsensitive(NewContext(harness), "checks")
	require.NoError(err)
	require.True(ok)

	cht, ok = table.(sql.CheckTable)
	require.True(ok)

	checks, err = cht.GetChecks(NewContext(harness))
	require.NoError(err)

	foundChk4 := false
	for _, check := range checks {
		if check.CheckExpression == "(b > 100)" {
			assert.True(t, len(check.Name) > 0, "empty check name")
			foundChk4 = true
			break
		}
	}
	assert.True(t, foundChk4, "check b > 100 not found")

	// Check statements in CREATE TABLE statements
	// TODO: <> gets parsed / serialized as NOT(=), needs to be fixed for full round trip compatibility
	RunQuery(t, e, harness, `
CREATE TABLE T2
(
  CHECK (c1 = c2),
  c1 INT CHECK (c1 > 10),
  c2 INT CONSTRAINT c2_positive CHECK (c2 > 0),
  c3 INT CHECK (c3 < 100),
  CONSTRAINT c1_nonzero CHECK (c1 = 0),
  CHECK (C1 > C3)
);`)

	table, ok, err = db.GetTableInsensitive(NewContext(harness), "t2")
	require.NoError(err)
	require.True(ok)

	cht, ok = table.(sql.CheckTable)
	require.True(ok)

	checks, err = cht.GetChecks(NewContext(harness))
	require.NoError(err)

	expectedCheckConds := []string{
		"(c1 = c2)",
		"(c1 > 10)",
		"(c2 > 0)",
		"(c3 < 100)",
		"(c1 = 0)",
		"(c1 > c3)",
	}

	var checkConds []string
	for _, check := range checks {
		checkConds = append(checkConds, check.CheckExpression)
	}

	assert.Equal(t, expectedCheckConds, checkConds)

	// Some faulty create statements
	AssertErr(t, e, harness, "ALTER TABLE t3 ADD CONSTRAINT chk2 CHECK (c > 0)", sql.ErrTableNotFound)
	AssertErr(t, e, harness, "ALTER TABLE checks ADD CONSTRAINT chk3 CHECK (d > 0)", sql.ErrColumnNotFound)

	AssertErr(t, e, harness, `
CREATE TABLE t4
(
  CHECK (c1 = c2),
  c1 INT CHECK (c1 > 10),
  c2 INT CONSTRAINT c2_positive CHECK (c2 > 0),
  CHECK (c1 > c3)
);`, sql.ErrTableColumnNotFound)

	// Test any scripts relevant to CheckConstraints. We do this separately from the rest of the scripts
	// as certain integrators might not implement check constraints.
	for _, script := range queries.CreateCheckConstraintsScripts {
		TestScript(t, harness, script)
	}
}

// todo(max): rewrite into []ScriptTest
func TestChecksOnInsert(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	RunQuery(t, e, harness, "CREATE TABLE t1 (a INTEGER PRIMARY KEY, b INTEGER, c varchar(20))")
	RunQuery(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk1 CHECK (b > 10) NOT ENFORCED")
	RunQuery(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (b > 0)")
	RunQuery(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk3 CHECK ((a + b) / 2 >= 1) ENFORCED")

	// TODO: checks get serialized as strings, which means that the String() method of functions is load-bearing.
	//  We do not have tests for all of them. Write some.
	RunQuery(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk4 CHECK (upper(c) = c) ENFORCED")
	RunQuery(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk5 CHECK (trim(c) = c) ENFORCED")
	RunQuery(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk6 CHECK (trim(leading ' ' from c) = c) ENFORCED")

	RunQuery(t, e, harness, "INSERT INTO t1 VALUES (1,1,'ABC')")

	TestQueryWithContext(t, ctx, e, `SELECT * FROM t1`, []sql.Row{
		{1, 1, "ABC"},
	}, nil, nil)
	AssertErr(t, e, harness, "INSERT INTO t1 (a,b) VALUES (0,0)", sql.ErrCheckConstraintViolated)
	AssertErr(t, e, harness, "INSERT INTO t1 (a,b) VALUES (0,1)", sql.ErrCheckConstraintViolated)
	AssertErr(t, e, harness, "INSERT INTO t1 (a,b,c) VALUES (2,2,'abc')", sql.ErrCheckConstraintViolated)
	AssertErr(t, e, harness, "INSERT INTO t1 (a,b,c) VALUES (2,2,'ABC ')", sql.ErrCheckConstraintViolated)
	AssertErr(t, e, harness, "INSERT INTO t1 (a,b,c) VALUES (2,2,' ABC')", sql.ErrCheckConstraintViolated)

	RunQuery(t, e, harness, "INSERT INTO t1 VALUES (2,2,'ABC')")
	RunQuery(t, e, harness, "INSERT INTO t1 (a,b) VALUES (4,NULL)")

	TestQueryWithContext(t, ctx, e, `SELECT * FROM t1`, []sql.Row{
		{1, 1, "ABC"},
		{2, 2, "ABC"},
		{4, nil, nil},
	}, nil, nil)

	RunQuery(t, e, harness, "CREATE TABLE t2 (a INTEGER PRIMARY KEY, b INTEGER)")
	RunQuery(t, e, harness, "INSERT INTO t2 VALUES (2,2),(3,3)")
	RunQuery(t, e, harness, "DELETE FROM t1")

	AssertErr(t, e, harness, "INSERT INTO t1 (a,b) select a - 2, b - 1 from t2", sql.ErrCheckConstraintViolated)
	RunQuery(t, e, harness, "INSERT INTO t1 (a,b) select a, b from t2")

	// Check that INSERT IGNORE correctly drops errors with check constraints and does not update the actual table.
	RunQuery(t, e, harness, "INSERT IGNORE INTO t1 VALUES (5,2, 'abc')")
	TestQueryWithContext(t, ctx, e, `SELECT count(*) FROM t1 where a = 5`, []sql.Row{{0}}, nil, nil)

	// One value is correctly accepted and the other value is not accepted due to a check constraint violation.
	// The accepted value is correctly added to the table.
	RunQuery(t, e, harness, "INSERT IGNORE INTO t1 VALUES (4,4, null), (5,2, 'abc')")
	TestQueryWithContext(t, ctx, e, `SELECT count(*) FROM t1 where a = 5`, []sql.Row{{0}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT count(*) FROM t1 where a = 4`, []sql.Row{{1}}, nil, nil)
}

// todo(max): rewrite into []ScriptTest
func TestChecksOnUpdate(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	RunQuery(t, e, harness, "CREATE TABLE t1 (a INTEGER PRIMARY KEY, b INTEGER)")
	RunQuery(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk1 CHECK (b > 10) NOT ENFORCED")
	RunQuery(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (b > 0)")
	RunQuery(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk3 CHECK ((a + b) / 2 >= 1) ENFORCED")
	RunQuery(t, e, harness, "INSERT INTO t1 VALUES (1,1)")

	TestQueryWithContext(t, ctx, e, `SELECT * FROM t1`, []sql.Row{
		{1, 1},
	}, nil, nil)

	AssertErr(t, e, harness, "UPDATE t1 set b = 0", sql.ErrCheckConstraintViolated)
	AssertErr(t, e, harness, "UPDATE t1 set a = 0, b = 1", sql.ErrCheckConstraintViolated)
	AssertErr(t, e, harness, "UPDATE t1 set b = 0 WHERE b = 1", sql.ErrCheckConstraintViolated)
	AssertErr(t, e, harness, "UPDATE t1 set a = 0, b = 1 WHERE b = 1", sql.ErrCheckConstraintViolated)

	TestQueryWithContext(t, ctx, e, `SELECT * FROM t1`, []sql.Row{
		{1, 1},
	}, nil, nil)
}

func TestDisallowedCheckConstraints(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	e := mustNewEngine(t, harness)
	defer e.Close()

	RunQuery(t, e, harness, "CREATE TABLE t1 (a INTEGER PRIMARY KEY, b INTEGER)")

	// non-deterministic functions
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (current_user = \"root@\")", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (user() = \"root@\")", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (now() > '2021')", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (current_date() > '2021')", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (uuid() > 'a')", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (database() = 'foo')", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (schema() = 'foo')", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (version() = 'foo')", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (last_insert_id() = 0)", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (rand() < .8)", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (row_count() = 0)", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (found_rows() = 0)", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (curdate() > '2021')", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (curtime() > '2021')", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (current_timestamp() > '2021')", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (connection_id() = 2)", sql.ErrInvalidConstraintFunctionNotSupported)

	// locks
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (get_lock('abc', 0) is null)", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (release_all_locks() is null)", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (release_lock('abc') is null)", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (is_free_lock('abc') is null)", sql.ErrInvalidConstraintFunctionNotSupported)
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (is_used_lock('abc') is null)", sql.ErrInvalidConstraintFunctionNotSupported)

	// subqueries
	AssertErr(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK ((select count(*) from t1) = 0)", sql.ErrInvalidConstraintSubqueryNotSupported)

	// TODO: need checks for stored procedures, also not allowed

	// Some spot checks on create table forms of the above
	AssertErr(t, e, harness, `
CREATE TABLE t3 (
	a int primary key CONSTRAINT chk2 CHECK (current_user = "root@")
)
`, sql.ErrInvalidConstraintFunctionNotSupported)

	AssertErr(t, e, harness, `
CREATE TABLE t3 (
	a int primary key,
	CHECK (current_user = "root@")
)
`, sql.ErrInvalidConstraintFunctionNotSupported)

	AssertErr(t, e, harness, `
CREATE TABLE t3 (
	a int primary key CONSTRAINT chk2 CHECK (a = (select count(*) from t1))
)
`, sql.ErrInvalidConstraintSubqueryNotSupported)

	AssertErr(t, e, harness, `
CREATE TABLE t3 (
	a int primary key,
	CHECK (a = (select count(*) from t1))
)
`, sql.ErrInvalidConstraintSubqueryNotSupported)
}

// todo(max): rewrite with []ScriptTest
func TestDropCheckConstraints(t *testing.T, harness Harness) {
	require := require.New(t)

	harness.Setup(setup.MydbData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	RunQuery(t, e, harness, "CREATE TABLE t1 (a INTEGER PRIMARY KEY, b INTEGER, c integer)")
	RunQuery(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk1 CHECK (a > 0)")
	RunQuery(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk2 CHECK (b > 0) NOT ENFORCED")
	RunQuery(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk3 CHECK (c > 0)")
	RunQuery(t, e, harness, "ALTER TABLE t1 DROP CONSTRAINT chk2")
	RunQuery(t, e, harness, "ALTER TABLE t1 DROP CHECK chk1")

	db, err := e.Analyzer.Catalog.Database(ctx, "mydb")
	require.NoError(err)

	table, ok, err := db.GetTableInsensitive(ctx, "t1")
	require.NoError(err)
	require.True(ok)

	cht, ok := table.(sql.CheckTable)
	require.True(ok)

	checks, err := cht.GetChecks(NewContext(harness))
	require.NoError(err)

	expected := []sql.CheckDefinition{
		{
			Name:            "chk3",
			CheckExpression: "(c > 0)",
			Enforced:        true,
		},
	}

	assert.Equal(t, expected, checks)

	RunQuery(t, e, harness, "ALTER TABLE t1 DROP CHECK chk3")

	// Some faulty drop statements
	AssertErr(t, e, harness, "ALTER TABLE t2 DROP CONSTRAINT chk2", sql.ErrTableNotFound)
	AssertErr(t, e, harness, "ALTER TABLE t1 DROP CONSTRAINT dne", sql.ErrUnknownConstraint)
}

func TestDropConstraints(t *testing.T, harness Harness) {
	require := require.New(t)

	harness.Setup(setup.MydbData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	RunQuery(t, e, harness, "CREATE TABLE t1 (a INTEGER PRIMARY KEY, b INTEGER, c integer)")
	RunQuery(t, e, harness, "CREATE TABLE t2 (a INTEGER PRIMARY KEY, b INTEGER, c integer, INDEX (b))")
	RunQuery(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT chk1 CHECK (a > 0)")
	RunQuery(t, e, harness, "ALTER TABLE t1 ADD CONSTRAINT fk1 FOREIGN KEY (a) REFERENCES t2(b)")

	db, err := e.Analyzer.Catalog.Database(ctx, "mydb")
	require.NoError(err)

	table, ok, err := db.GetTableInsensitive(ctx, "t1")
	require.NoError(err)
	require.True(ok)

	cht, ok := table.(sql.CheckTable)
	require.True(ok)

	checks, err := cht.GetChecks(NewContext(harness))
	require.NoError(err)

	expected := []sql.CheckDefinition{
		{
			Name:            "chk1",
			CheckExpression: "(a > 0)",
			Enforced:        true,
		},
	}
	assert.Equal(t, expected, checks)

	fkt, ok := table.(sql.ForeignKeyTable)
	require.True(ok)

	fks, err := fkt.GetDeclaredForeignKeys(NewContext(harness))
	require.NoError(err)

	expectedFks := []sql.ForeignKeyConstraint{
		{
			Name:           "fk1",
			Database:       "mydb",
			Table:          "t1",
			Columns:        []string{"a"},
			ParentDatabase: "mydb",
			ParentTable:    "t2",
			ParentColumns:  []string{"b"},
			OnUpdate:       "DEFAULT",
			OnDelete:       "DEFAULT",
			IsResolved:     true,
		},
	}
	assert.Equal(t, expectedFks, fks)

	RunQuery(t, e, harness, "ALTER TABLE t1 DROP CONSTRAINT chk1")

	table, ok, err = db.GetTableInsensitive(ctx, "t1")
	require.NoError(err)
	require.True(ok)

	cht, ok = table.(sql.CheckTable)
	require.True(ok)

	checks, err = cht.GetChecks(NewContext(harness))
	require.NoError(err)

	expected = []sql.CheckDefinition{}
	assert.Equal(t, expected, checks)

	fkt, ok = table.(sql.ForeignKeyTable)
	require.True(ok)

	fks, err = fkt.GetDeclaredForeignKeys(NewContext(harness))
	require.NoError(err)

	expectedFks = []sql.ForeignKeyConstraint{
		{
			Name:           "fk1",
			Database:       "mydb",
			Table:          "t1",
			Columns:        []string{"a"},
			ParentDatabase: "mydb",
			ParentTable:    "t2",
			ParentColumns:  []string{"b"},
			OnUpdate:       "DEFAULT",
			OnDelete:       "DEFAULT",
			IsResolved:     true,
		},
	}
	assert.Equal(t, expectedFks, fks)

	RunQuery(t, e, harness, "ALTER TABLE t1 DROP CONSTRAINT fk1")

	table, ok, err = db.GetTableInsensitive(ctx, "t1")
	require.NoError(err)
	require.True(ok)

	cht, ok = table.(sql.CheckTable)
	require.True(ok)

	checks, err = cht.GetChecks(NewContext(harness))
	require.NoError(err)
	assert.Len(t, checks, 0)

	fkt, ok = table.(sql.ForeignKeyTable)
	require.True(ok)

	fks, err = fkt.GetDeclaredForeignKeys(NewContext(harness))
	require.NoError(err)
	assert.Len(t, fks, 0)

	// Some error statements
	AssertErr(t, e, harness, "ALTER TABLE t3 DROP CONSTRAINT fk1", sql.ErrTableNotFound)
	AssertErr(t, e, harness, "ALTER TABLE t1 DROP CONSTRAINT fk1", sql.ErrUnknownConstraint)
}

func TestWindowFunctions(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	RunQuery(t, e, harness, "CREATE TABLE t1 (a INTEGER PRIMARY KEY, b INTEGER, c integer)")
	RunQuery(t, e, harness, "INSERT INTO t1 VALUES (0,0,0), (1,1,1), (2,2,0), (3,0,0), (4,1,0), (5,3,0)")

	TestQueryWithContext(t, ctx, e, `SELECT a, percent_rank() over (order by b) FROM t1 order by a`, []sql.Row{
		{0, 0.0},
		{1, 0.4},
		{2, 0.8},
		{3, 0.0},
		{4, 0.4},
		{5, 1.0},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `SELECT a, percent_rank() over (order by b desc) FROM t1 order by a`, []sql.Row{
		{0, 0.8},
		{1, 0.4},
		{2, 0.2},
		{3, 0.8},
		{4, 0.4},
		{5, 0.0},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `SELECT a, percent_rank() over (partition by c order by b) FROM t1 order by a`, []sql.Row{
		{0, 0.0},
		{1, 0.0},
		{2, 0.75},
		{3, 0.0},
		{4, 0.5},
		{5, 1.0},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `SELECT a, percent_rank() over (partition by b order by c) FROM t1 order by a`, []sql.Row{
		{0, 0.0},
		{1, 1.0},
		{2, 0.0},
		{3, 0.0},
		{4, 0.0},
		{5, 0.0},
	}, nil, nil)

	// no order by clause -> all rows are peers
	TestQueryWithContext(t, ctx, e, `SELECT a, percent_rank() over (partition by b) FROM t1 order by a`, []sql.Row{
		{0, 0.0},
		{1, 0.0},
		{2, 0.0},
		{3, 0.0},
		{4, 0.0},
		{5, 0.0},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `SELECT a, first_value(b) over (partition by c order by b) FROM t1 order by a`, []sql.Row{
		{0, 0},
		{1, 1},
		{2, 0},
		{3, 0},
		{4, 0},
		{5, 0},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `SELECT a, first_value(a) over (partition by b order by a ASC, c ASC) FROM t1 order by a`, []sql.Row{
		{0, 0},
		{1, 1},
		{2, 2},
		{3, 0},
		{4, 1},
		{5, 5},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `SELECT a, first_value(a-1) over (partition by b order by a ASC, c ASC) FROM t1 order by a`, []sql.Row{
		{0, -1},
		{1, 0},
		{2, 1},
		{3, -1},
		{4, 0},
		{5, 4},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `SELECT a, first_value(c) over (partition by b) FROM t1 order by a*b,a`, []sql.Row{
		{0, 0},
		{3, 0},
		{1, 1},
		{2, 0},
		{4, 1},
		{5, 0},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `SELECT a, lag(a) over (partition by c order by a) FROM t1 order by a`, []sql.Row{
		{0, nil},
		{1, nil},
		{2, 0},
		{3, 2},
		{4, 3},
		{5, 4},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `SELECT a, lag(a, 1) over (partition by c order by a) FROM t1 order by a`, []sql.Row{
		{0, nil},
		{1, nil},
		{2, 0},
		{3, 2},
		{4, 3},
		{5, 4},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `SELECT a, lag(a+2) over (partition by c order by a) FROM t1 order by a`, []sql.Row{
		{0, nil},
		{1, nil},
		{2, 2},
		{3, 4},
		{4, 5},
		{5, 6},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `SELECT a, lag(a, 1, a-1) over (partition by c order by a) FROM t1 order by a`, []sql.Row{
		{0, -1},
		{1, 0},
		{2, 0},
		{3, 2},
		{4, 3},
		{5, 4},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `SELECT a, lag(a, 0) over (partition by c order by a) FROM t1 order by a`, []sql.Row{
		{0, 0},
		{1, 1},
		{2, 2},
		{3, 3},
		{4, 4},
		{5, 5},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `SELECT a, lag(a, 1, -1) over (partition by c order by a) FROM t1 order by a`, []sql.Row{
		{0, -1},
		{1, -1},
		{2, 0},
		{3, 2},
		{4, 3},
		{5, 4},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `SELECT a, lag(a, 3, -1) over (partition by c order by a) FROM t1 order by a`, []sql.Row{
		{0, -1},
		{1, -1},
		{2, -1},
		{3, -1},
		{4, 0},
		{5, 2},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `SELECT a, lag('s') over (partition by c order by a) FROM t1 order by a`, []sql.Row{
		{0, nil},
		{1, nil},
		{2, "s"},
		{3, "s"},
		{4, "s"},
		{5, "s"},
	}, nil, nil)

	AssertErr(t, e, harness, "SELECT a, lag(a, -1) over (partition by c) FROM t1", expression.ErrInvalidOffset)
	AssertErr(t, e, harness, "SELECT a, lag(a, 's') over (partition by c) FROM t1", expression.ErrInvalidOffset)

}

func TestWindowRowFrames(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	RunQuery(t, e, harness, "CREATE TABLE a (x INTEGER PRIMARY KEY, y INTEGER, z INTEGER)")
	RunQuery(t, e, harness, "INSERT INTO a VALUES (0,0,0), (1,1,0), (2,2,0), (3,0,0), (4,1,0), (5,3,0)")
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x rows unbounded preceding) FROM a order by x`, []sql.Row{{float64(0)}, {float64(1)}, {float64(3)}, {float64(3)}, {float64(4)}, {float64(7)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x rows current row) FROM a order by x`, []sql.Row{{float64(0)}, {float64(1)}, {float64(2)}, {float64(0)}, {float64(1)}, {float64(3)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x rows 2 preceding) FROM a order by x`, []sql.Row{{float64(0)}, {float64(1)}, {float64(3)}, {float64(3)}, {float64(3)}, {float64(4)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x rows between current row and 1 following) FROM a order by x`, []sql.Row{{float64(1)}, {float64(3)}, {float64(2)}, {float64(1)}, {float64(4)}, {float64(3)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x rows between 1 preceding and current row) FROM a order by x`, []sql.Row{{float64(0)}, {float64(1)}, {float64(3)}, {float64(2)}, {float64(1)}, {float64(4)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x rows between current row and 2 following) FROM a order by x`, []sql.Row{{float64(3)}, {float64(3)}, {float64(3)}, {float64(4)}, {float64(4)}, {float64(3)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x rows between current row and current row) FROM a order by x`, []sql.Row{{float64(0)}, {float64(1)}, {float64(2)}, {float64(0)}, {float64(1)}, {float64(3)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x rows between current row and unbounded following) FROM a order by x`, []sql.Row{{float64(7)}, {float64(7)}, {float64(6)}, {float64(4)}, {float64(4)}, {float64(3)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x rows between 1 preceding and 1 following) FROM a order by x`, []sql.Row{{float64(1)}, {float64(3)}, {float64(3)}, {float64(3)}, {float64(4)}, {float64(4)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x rows between 1 preceding and unbounded following) FROM a order by x`, []sql.Row{{float64(7)}, {float64(7)}, {float64(7)}, {float64(6)}, {float64(4)}, {float64(4)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x rows between unbounded preceding and unbounded following) FROM a order by x`, []sql.Row{{float64(7)}, {float64(7)}, {float64(7)}, {float64(7)}, {float64(7)}, {float64(7)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x rows between 2 preceding and 1 preceding) FROM a order by x`, []sql.Row{{nil}, {float64(0)}, {float64(1)}, {float64(3)}, {float64(2)}, {float64(1)}}, nil, nil)
}

func TestWindowRangeFrames(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	RunQuery(t, e, harness, "CREATE TABLE a (x INTEGER PRIMARY KEY, y INTEGER, z INTEGER)")
	RunQuery(t, e, harness, "INSERT INTO a VALUES (0,0,0), (1,1,0), (2,2,0), (3,0,0), (4,1,0), (5,3,0)")
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x range unbounded preceding) FROM a order by x`, []sql.Row{{float64(0)}, {float64(1)}, {float64(3)}, {float64(3)}, {float64(4)}, {float64(7)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x range current row) FROM a order by x`, []sql.Row{{float64(0)}, {float64(1)}, {float64(2)}, {float64(0)}, {float64(1)}, {float64(3)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x range 2 preceding) FROM a order by x`, []sql.Row{{float64(0)}, {float64(1)}, {float64(3)}, {float64(3)}, {float64(3)}, {float64(4)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x range between current row and 1 following) FROM a order by x`, []sql.Row{{float64(1)}, {float64(3)}, {float64(2)}, {float64(1)}, {float64(4)}, {float64(3)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x range between 1 preceding and current row) FROM a order by x`, []sql.Row{{float64(0)}, {float64(1)}, {float64(3)}, {float64(2)}, {float64(1)}, {float64(4)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x range between current row and 2 following) FROM a order by x`, []sql.Row{{float64(3)}, {float64(3)}, {float64(3)}, {float64(4)}, {float64(4)}, {float64(3)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x range between current row and current row) FROM a order by x`, []sql.Row{{float64(0)}, {float64(1)}, {float64(2)}, {float64(0)}, {float64(1)}, {float64(3)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x range between current row and unbounded following) FROM a order by x`, []sql.Row{{float64(7)}, {float64(7)}, {float64(6)}, {float64(4)}, {float64(4)}, {float64(3)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x range between 1 preceding and 1 following) FROM a order by x`, []sql.Row{{float64(1)}, {float64(3)}, {float64(3)}, {float64(3)}, {float64(4)}, {float64(4)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x range between 1 preceding and unbounded following) FROM a order by x`, []sql.Row{{float64(7)}, {float64(7)}, {float64(7)}, {float64(6)}, {float64(4)}, {float64(4)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x range between unbounded preceding and unbounded following) FROM a order by x`, []sql.Row{{float64(7)}, {float64(7)}, {float64(7)}, {float64(7)}, {float64(7)}, {float64(7)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by x range between 2 preceding and 1 preceding) FROM a order by x`, []sql.Row{{nil}, {float64(0)}, {float64(1)}, {float64(3)}, {float64(2)}, {float64(1)}}, nil, nil)

	// fixed frame size, 3 days
	RunQuery(t, e, harness, "CREATE TABLE b (x INTEGER PRIMARY KEY, y INTEGER, z INTEGER, date DATE)")
	RunQuery(t, e, harness, "INSERT INTO b VALUES (0,0,0,'2022-01-26'), (1,0,0,'2022-01-27'), (2,0,0, '2022-01-28'), (3,1,0,'2022-01-29'), (4,1,0,'2022-01-30'), (5,3,0,'2022-01-31')")
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by date range between interval 2 DAY preceding and interval 1 DAY preceding) FROM b order by x`, []sql.Row{{nil}, {float64(0)}, {float64(0)}, {float64(0)}, {float64(1)}, {float64(2)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by date range between interval 1 DAY preceding and interval 1 DAY following) FROM b order by x`, []sql.Row{{float64(0)}, {float64(0)}, {float64(1)}, {float64(2)}, {float64(5)}, {float64(4)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by date range between interval 1 DAY following and interval 2 DAY following) FROM b order by x`, []sql.Row{{float64(0)}, {float64(1)}, {float64(2)}, {float64(4)}, {float64(3)}, {nil}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by date range interval 1 DAY preceding) FROM b order by x`, []sql.Row{{float64(0)}, {float64(0)}, {float64(0)}, {float64(1)}, {float64(2)}, {float64(4)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by date range between interval 1 DAY preceding and current row) FROM b order by x`, []sql.Row{{float64(0)}, {float64(0)}, {float64(0)}, {float64(1)}, {float64(2)}, {float64(4)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by date range between interval 1 DAY preceding and unbounded following) FROM b order by x`, []sql.Row{{float64(5)}, {float64(5)}, {float64(5)}, {float64(5)}, {float64(5)}, {float64(4)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by date range between unbounded preceding and interval 1 DAY following) FROM b order by x`, []sql.Row{{float64(0)}, {float64(0)}, {float64(1)}, {float64(2)}, {float64(5)}, {float64(5)}}, nil, nil)

	// variable range size, 1 or many days
	RunQuery(t, e, harness, "CREATE TABLE c (x INTEGER PRIMARY KEY, y INTEGER, z INTEGER, date DATE)")
	RunQuery(t, e, harness, "INSERT INTO c VALUES (0,0,0,'2022-01-26'), (1,0,0,'2022-01-26'), (2,0,0, '2022-01-26'), (3,1,0,'2022-01-27'), (4,1,0,'2022-01-29'), (5,3,0,'2022-01-30'), (6,0,0, '2022-02-03'), (7,1,0,'2022-02-03'), (8,1,0,'2022-02-04'), (9,3,0,'2022-02-04')")
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by date range between interval '2' DAY preceding and interval '1' DAY preceding) FROM c order by x`, []sql.Row{{nil}, {nil}, {nil}, {float64(0)}, {float64(1)}, {float64(1)}, {nil}, {nil}, {float64(1)}, {float64(1)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by date range between interval '1' DAY preceding and interval '1' DAY following) FROM c order by x`, []sql.Row{{float64(1)}, {float64(1)}, {float64(1)}, {float64(1)}, {float64(4)}, {float64(4)}, {float64(5)}, {float64(5)}, {float64(5)}, {float64(5)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT first_value(x) over (partition by z order by date range interval '1' DAY preceding) FROM c order by x`, []sql.Row{{0}, {0}, {0}, {0}, {4}, {4}, {6}, {6}, {6}, {6}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by date range between interval '1' DAY preceding and current row) FROM c order by x`, []sql.Row{{float64(0)}, {float64(0)}, {float64(0)}, {float64(1)}, {float64(1)}, {float64(4)}, {float64(1)}, {float64(1)}, {float64(5)}, {float64(5)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT avg(y) over (partition by z order by date range between interval '1' DAY preceding and unbounded following) FROM c order by x`, []sql.Row{{float64(1)}, {float64(1)}, {float64(1)}, {float64(1)}, {float64(3) / float64(2)}, {float64(3) / float64(2)}, {float64(5) / float64(4)}, {float64(5) / float64(4)}, {float64(5) / float64(4)}, {float64(5) / float64(4)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (partition by z order by date range between unbounded preceding and interval '1' DAY following) FROM c order by x`, []sql.Row{{float64(1)}, {float64(1)}, {float64(1)}, {float64(1)}, {float64(5)}, {float64(5)}, {float64(10)}, {float64(10)}, {float64(10)}, {float64(10)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT count(y) over (partition by z order by date range between interval '1' DAY following and interval '2' DAY following) FROM c order by x`, []sql.Row{{1}, {1}, {1}, {1}, {1}, {0}, {2}, {2}, {0}, {0}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT count(y) over (partition by z order by date range between interval '1' DAY preceding and interval '2' DAY following) FROM c order by x`, []sql.Row{{4}, {4}, {4}, {5}, {2}, {2}, {4}, {4}, {4}, {4}}, nil, nil)

	AssertErr(t, e, harness, "SELECT sum(y) over (partition by z range between unbounded preceding and interval '1' DAY following) FROM c order by x", aggregation.ErrRangeInvalidOrderBy)
	AssertErr(t, e, harness, "SELECT sum(y) over (partition by z order by date range interval 'e' DAY preceding) FROM c order by x", sql.ErrInvalidValue)
}

func TestNamedWindows(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	RunQuery(t, e, harness, "CREATE TABLE a (x INTEGER PRIMARY KEY, y INTEGER, z INTEGER)")
	RunQuery(t, e, harness, "INSERT INTO a VALUES (0,0,0), (1,1,0), (2,2,0), (3,0,0), (4,1,0), (5,3,0)")

	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (w1) FROM a WINDOW w1 as (order by z) order by x`, []sql.Row{{float64(0)}, {float64(1)}, {float64(3)}, {float64(3)}, {float64(4)}, {float64(7)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (w1) FROM a WINDOW w1 as (partition by z) order by x`, []sql.Row{{float64(0)}, {float64(1)}, {float64(3)}, {float64(3)}, {float64(4)}, {float64(7)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over w FROM a WINDOW w as (partition by z order by x rows unbounded preceding) order by x`, []sql.Row{{float64(0)}, {float64(1)}, {float64(3)}, {float64(3)}, {float64(4)}, {float64(7)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over w FROM a WINDOW w as (partition by z order by x rows current row) order by x`, []sql.Row{{float64(0)}, {float64(1)}, {float64(2)}, {float64(0)}, {float64(1)}, {float64(3)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT sum(y) over (w) FROM a WINDOW w as (partition by z order by x rows 2 preceding) order by x`, []sql.Row{{float64(0)}, {float64(1)}, {float64(3)}, {float64(3)}, {float64(3)}, {float64(4)}}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT row_number() over (w3) FROM a WINDOW w3 as (w2), w2 as (w1), w1 as (partition by z) order by x`, []sql.Row{{int64(1)}, {int64(2)}, {int64(3)}, {int64(4)}, {int64(5)}, {int64(6)}}, nil, nil)

	// errors
	AssertErr(t, e, harness, "SELECT sum(y) over (w1 partition by x) FROM a WINDOW w1 as (partition by z) order by x", sql.ErrInvalidWindowInheritance)
	AssertErr(t, e, harness, "SELECT sum(y) over (w1 order by x) FROM a WINDOW w1 as (order by z) order by x", sql.ErrInvalidWindowInheritance)
	AssertErr(t, e, harness, "SELECT sum(y) over (w1 rows unbounded preceding) FROM a WINDOW w1 as (range unbounded preceding) order by x", sql.ErrInvalidWindowInheritance)
	AssertErr(t, e, harness, "SELECT sum(y) over (w3) FROM a WINDOW w1 as (w2), w2 as (w3), w3 as (w1) order by x", sql.ErrCircularWindowInheritance)

	// TODO parser needs to differentiate between window replacement and copying -- window frames can't be copied
	//AssertErr(t, e, harness, "SELECT sum(y) over w FROM a WINDOW (w) as (partition by z order by x rows unbounded preceding) order by x", sql.ErrInvalidWindowInheritance)
}

func TestNaturalJoin(t *testing.T, harness Harness) {
	harness.Setup([]setup.SetupScript{{
		"create database mydb",
		"use mydb",
		"create table t1 (a text primary key, b text, c text)",
		"create table t2 (a text primary key, b text, d text)",
		"insert into t1 values ('a_1', 'b_1', 'c_1'), ('a_2', 'b_2', 'c_2'), ('a_3', 'b_3', 'c_3')",
		"insert into t2 values ('a_1', 'b_1', 'd_1'), ('a_2', 'b_2', 'd_2'), ('a_3', 'b_3', 'd_3')",
	}})
	e := mustNewEngine(t, harness)
	defer e.Close()

	TestQuery(t, harness, `SELECT * FROM t1 NATURAL JOIN t2`, []sql.Row{
		{"a_1", "b_1", "c_1", "d_1"},
		{"a_2", "b_2", "c_2", "d_2"},
		{"a_3", "b_3", "c_3", "d_3"},
	}, nil, nil)
}

func TestNaturalJoinEqual(t *testing.T, harness Harness) {
	harness.Setup([]setup.SetupScript{{
		"create database mydb",
		"use mydb",
		"create table t1 (a text primary key, b text, c text)",
		"create table t2 (a text primary key, b text, c text)",
		"insert into t1 values ('a_1', 'b_1', 'c_1'), ('a_2', 'b_2', 'c_2'), ('a_3', 'b_3', 'c_3')",
		"insert into t2 values ('a_1', 'b_1', 'c_1'), ('a_2', 'b_2', 'c_2'), ('a_3', 'b_3', 'c_3')",
	}})
	e := mustNewEngine(t, harness)
	defer e.Close()
	TestQuery(t, harness, `SELECT * FROM t1 NATURAL JOIN t2`, []sql.Row{
		{"a_1", "b_1", "c_1"},
		{"a_2", "b_2", "c_2"},
		{"a_3", "b_3", "c_3"},
	}, nil, nil)
}

func TestNaturalJoinDisjoint(t *testing.T, harness Harness) {
	harness.Setup([]setup.SetupScript{{
		"create database mydb",
		"use mydb",
		"create table t1 (a text primary key)",
		"create table t2 (b text primary key)",
		"insert into t1 values ('a1'), ('a2'), ('a3')",
		"insert into t2 values ('b1'), ('b2'), ('b3')",
	}})
	e := mustNewEngine(t, harness)
	defer e.Close()
	TestQuery(t, harness, `SELECT * FROM t1 NATURAL JOIN t2`, []sql.Row{
		{"a1", "b1"},
		{"a1", "b2"},
		{"a1", "b3"},
		{"a2", "b1"},
		{"a2", "b2"},
		{"a2", "b3"},
		{"a3", "b1"},
		{"a3", "b2"},
		{"a3", "b3"},
	}, nil, nil)
}

func TestInnerNestedInNaturalJoins(t *testing.T, harness Harness) {
	harness.Setup([]setup.SetupScript{{
		"create database mydb",
		"use mydb",
		"create table table1 (i int, f float, t text)",
		"create table table2 (i2 int, f2 float, t2 text)",
		"create table table3 (i int, f2 float, t3 text)",
		"insert into table1 values (1, 2.1000, 'table1'), (1, 2.1000, 'table1'), (10, 2.1000, 'table1')",
		"insert into table2 values (1, 2.1000, 'table2'), (1, 2.2000, 'table2'), (20, 2.2000, 'table2')",
		"insert into table3 values (1, 2.2000, 'table3'), (2, 2.2000, 'table3'), (30, 2.2000, 'table3')",
	}})
	e := mustNewEngine(t, harness)
	defer e.Close()

	TestQuery(t, harness, `SELECT table1.i, t, i2, t2, t3 FROM table1 INNER JOIN table2 ON table1.i = table2.i2 NATURAL JOIN table3`, []sql.Row{
		{int32(1), "table1", int32(1), "table2", "table3"},
		{int32(1), "table1", int32(1), "table2", "table3"},
	}, nil, nil)
}

func TestVariables(t *testing.T, harness Harness) {
	for _, query := range queries.VariableQueries {
		TestScript(t, harness, query)
	}
	// Test session pulling from global
	engine := sqle.NewDefault(harness.NewDatabaseProvider())
	ctx1 := sql.NewEmptyContext()
	for _, assertion := range []queries.ScriptTestAssertion{
		{
			Query:    "SELECT @@select_into_buffer_size",
			Expected: []sql.Row{{131072}},
		},
		{
			Query:    "SELECT @@GLOBAL.select_into_buffer_size",
			Expected: []sql.Row{{131072}},
		},
		{
			Query:    "SET GLOBAL select_into_buffer_size = 9001",
			Expected: []sql.Row{{}},
		},
		{
			Query:    "SELECT @@SESSION.select_into_buffer_size",
			Expected: []sql.Row{{131072}},
		},
		{
			Query:    "SELECT @@GLOBAL.select_into_buffer_size",
			Expected: []sql.Row{{9001}},
		},
		{
			Query:    "SET @@GLOBAL.select_into_buffer_size = 9002",
			Expected: []sql.Row{{}},
		},
		{
			Query:    "SELECT @@GLOBAL.select_into_buffer_size",
			Expected: []sql.Row{{9002}},
		},
	} {
		TestQueryWithContext(t, ctx1, engine, assertion.Query, assertion.Expected, nil, nil)
	}
	ctx2 := sql.NewEmptyContext()
	for _, assertion := range []queries.ScriptTestAssertion{
		{
			Query:    "SELECT @@select_into_buffer_size",
			Expected: []sql.Row{{9002}},
		},
		{
			Query:    "SELECT @@GLOBAL.select_into_buffer_size",
			Expected: []sql.Row{{9002}},
		},
	} {
		TestQueryWithContext(t, ctx2, engine, assertion.Query, assertion.Expected, nil, nil)
	}
}

func TestPreparedInsert(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData)
	e := mustNewEngine(t, harness)
	defer e.Close()

	tests := []queries.ScriptTest{
		{
			Name: "simple insert",
			SetUpScript: []string{
				"create table test (pk int primary key, value int)",
				"insert into test values (0,0)",
			},
			Assertions: []queries.ScriptTestAssertion{
				{
					Query: "insert into test values (?, ?)",
					Bindings: map[string]sql.Expression{
						"v1": expression.NewLiteral(1, sql.Int64),
						"v2": expression.NewLiteral(1, sql.Int64),
					},
					Expected: []sql.Row{
						{sql.OkResult{RowsAffected: 1}},
					},
				},
			},
		},
		{
			Name: "Insert on duplicate key",
			SetUpScript: []string{
				`CREATE TABLE users (
  				id varchar(42) PRIMARY KEY
			)`,
				`CREATE TABLE nodes (
			    id varchar(42) PRIMARY KEY,
			    owner varchar(42),
			    status varchar(12),
			    timestamp bigint NOT NULL,
			    FOREIGN KEY(owner) REFERENCES users(id)
			)`,
				"INSERT INTO users values ('milo'), ('dabe')",
				"INSERT INTO nodes values ('id1', 'milo', 'off', 1)",
			},
			Assertions: []queries.ScriptTestAssertion{
				{
					Query: "insert into nodes(id,owner,status,timestamp) values(?, ?, ?, ?) on duplicate key update owner=?,status=?",
					Bindings: map[string]sql.Expression{
						"v1": expression.NewLiteral("id1", sql.Text),
						"v2": expression.NewLiteral("dabe", sql.Text),
						"v3": expression.NewLiteral("off", sql.Text),
						"v4": expression.NewLiteral(2, sql.Int64),
						"v5": expression.NewLiteral("milo", sql.Text),
						"v6": expression.NewLiteral("on", sql.Text),
					},
					Expected: []sql.Row{
						{sql.OkResult{RowsAffected: 2}},
					},
				},
				{
					Query: "insert into nodes(id,owner,status,timestamp) values(?, ?, ?, ?) on duplicate key update owner=?,status=?",
					Bindings: map[string]sql.Expression{
						"v1": expression.NewLiteral("id2", sql.Text),
						"v2": expression.NewLiteral("dabe", sql.Text),
						"v3": expression.NewLiteral("off", sql.Text),
						"v4": expression.NewLiteral(3, sql.Int64),
						"v5": expression.NewLiteral("milo", sql.Text),
						"v6": expression.NewLiteral("on", sql.Text),
					},
					Expected: []sql.Row{
						{sql.OkResult{RowsAffected: 1}},
					},
				},
				{
					Query: "select * from nodes",
					Expected: []sql.Row{
						{"id1", "milo", "on", 1},
						{"id2", "dabe", "off", 3},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		TestScript(t, harness, tt)
	}
}

// Runs tests on SHOW TABLE STATUS queries.
func TestShowTableStatus(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData, setup.OthertableData)
	for _, tt := range queries.ShowTableStatusQueries {
		TestQuery(t, harness, tt.Query, tt.Expected, nil, nil)
	}
}

func TestDateParse(t *testing.T, harness Harness) {
	harness.Setup()
	for _, tt := range queries.DateParseQueries {
		TestQuery(t, harness, tt.Query, tt.Expected, nil, nil)
	}
}

// Runs tests on SHOW TABLE STATUS queries.
func TestShowTableStatusPrepared(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData, setup.OthertableData)
	for _, tt := range queries.ShowTableStatusQueries {
		TestPreparedQuery(t, harness, tt.Query, tt.Expected, nil)
	}
}

func TestVariableErrors(t *testing.T, harness Harness) {
	harness.Setup()
	e := mustNewEngine(t, harness)
	defer e.Close()
	for _, test := range queries.VariableErrorTests {
		AssertErr(t, e, harness, test.Query, test.ExpectedErr)
	}
}

func TestWarnings(t *testing.T, harness Harness) {
	var queries = []queries.QueryTest{
		{
			Query: `
			SHOW WARNINGS
			`,
			Expected: []sql.Row{
				{"", 3, ""},
				{"", 2, ""},
				{"", 1, ""},
			},
		},
		{
			Query: `
			SHOW WARNINGS LIMIT 1
			`,
			Expected: []sql.Row{
				{"", 3, ""},
			},
		},
		{
			Query: `
			SHOW WARNINGS LIMIT 1,2
			`,
			Expected: []sql.Row{
				{"", 2, ""},
				{"", 1, ""},
			},
		},
		{
			Query: `
			SHOW WARNINGS LIMIT 0
			`,
			Expected: nil,
		},
		{
			Query: `
			SHOW WARNINGS LIMIT 2,1
			`,
			Expected: []sql.Row{
				{"", 1, ""},
			},
		},
		{
			Query: `
			SHOW WARNINGS LIMIT 10
			`,
			Expected: []sql.Row{
				{"", 3, ""},
				{"", 2, ""},
				{"", 1, ""},
			},
		},
		{
			Query: `
			SHOW WARNINGS LIMIT 10,1
			`,
			Expected: nil,
		},
	}

	harness.Setup()
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	ctx.Session.Warn(&sql.Warning{Code: 1})
	ctx.Session.Warn(&sql.Warning{Code: 2})
	ctx.Session.Warn(&sql.Warning{Code: 3})

	for _, tt := range queries {
		TestQueryWithContext(t, ctx, e, tt.Query, tt.Expected, nil, nil)
	}
}

func TestClearWarnings(t *testing.T, harness Harness) {
	require := require.New(t)
	harness.Setup(setup.Mytable...)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	sch, iter, err := e.Query(ctx, "-- some empty query as a comment")
	require.NoError(err)
	err = iter.Close(ctx)
	require.NoError(err)

	sch, iter, err = e.Query(ctx, "-- some empty query as a comment")
	require.NoError(err)
	err = iter.Close(ctx)
	require.NoError(err)

	sch, iter, err = e.Query(ctx, "-- some empty query as a comment")
	require.NoError(err)
	err = iter.Close(ctx)
	require.NoError(err)

	sch, iter, err = e.Query(ctx, "SHOW WARNINGS")
	require.NoError(err)
	rows, err := sql.RowIterToRows(ctx, sch, iter)
	require.NoError(err)
	err = iter.Close(ctx)
	require.NoError(err)
	require.Equal(3, len(rows))

	sch, iter, err = e.Query(ctx, "SHOW WARNINGS LIMIT 1")
	require.NoError(err)
	rows, err = sql.RowIterToRows(ctx, sch, iter)
	require.NoError(err)
	err = iter.Close(ctx)
	require.NoError(err)
	require.Equal(1, len(rows))

	sch, iter, err = e.Query(ctx, "SELECT * FROM mytable LIMIT 1")
	require.NoError(err)
	_, err = sql.RowIterToRows(ctx, sch, iter)
	require.NoError(err)
	err = iter.Close(ctx)
	require.NoError(err)

	require.Equal(0, len(ctx.Session.Warnings()))
}

func TestUse(t *testing.T, harness Harness) {
	require := require.New(t)
	harness.Setup(setup.MydbData, setup.MytableData, setup.FooData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)
	require.Equal("mydb", ctx.GetCurrentDatabase())

	_, _, err := e.Query(ctx, "USE bar")
	require.Error(err)

	require.Equal("mydb", ctx.GetCurrentDatabase())

	sch, iter, err := e.Query(ctx, "USE foo")
	require.NoError(err)

	rows, err := sql.RowIterToRows(ctx, sch, iter)
	require.NoError(err)
	require.Len(rows, 0)

	require.Equal("foo", ctx.GetCurrentDatabase())

	_, _, err = e.Query(ctx, "USE MYDB")
	require.NoError(err)

	require.Equal("mydb", ctx.GetCurrentDatabase())
}

// TestConcurrentTransactions tests that two concurrent processes/transactions can successfully execute without early
// cancellation.
func TestConcurrentTransactions(t *testing.T, harness Harness) {
	require := require.New(t)
	harness.Setup(setup.MydbData)
	e := mustNewEngine(t, harness)
	defer e.Close()

	RunQuery(t, e, harness, `CREATE TABLE a (x int primary key, y int)`)

	clientSessionA := NewSession(harness)
	clientSessionA.ProcessList = sqle.NewProcessList()

	clientSessionB := NewSession(harness)
	clientSessionB.ProcessList = sqle.NewProcessList()

	var err error
	// We want to add the query to the process list to represent the full workflow.
	clientSessionA, err = clientSessionA.ProcessList.AddProcess(clientSessionA, "INSERT INTO a VALUES (1,1)")
	require.NoError(err)
	sch, iter, err := e.Query(clientSessionA, "INSERT INTO a VALUES (1,1)")
	require.NoError(err)

	clientSessionB, err = clientSessionB.ProcessList.AddProcess(clientSessionB, "INSERT INTO a VALUES (2,2)")
	require.NoError(err)
	sch2, iter2, err := e.Query(clientSessionB, "INSERT INTO a VALUES (2,2)")
	require.NoError(err)

	rows, err := sql.RowIterToRows(clientSessionA, sch, iter)
	require.NoError(err)
	require.Len(rows, 1)

	rows, err = sql.RowIterToRows(clientSessionB, sch2, iter2)
	require.NoError(err)
	require.Len(rows, 1)
}

func TestNoDatabaseSelected(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)
	ctx.SetCurrentDatabase("")

	AssertErrWithCtx(t, e, ctx, "create table a (b int primary key)", sql.ErrNoDatabaseSelected)
	AssertErrWithCtx(t, e, ctx, "show tables", sql.ErrNoDatabaseSelected)
	AssertErrWithCtx(t, e, ctx, "show triggers", sql.ErrNoDatabaseSelected)

	_, _, err := e.Query(ctx, "ROLLBACK")
	require.NoError(t, err)
}

func TestSessionSelectLimit(t *testing.T, harness Harness) {
	q := []queries.QueryTest{
		{
			Query:    "SELECT * FROM mytable ORDER BY i",
			Expected: []sql.Row{{int64(1), "first row"}},
		},
		{
			Query: "SELECT * FROM mytable ORDER BY i LIMIT 2",
			Expected: []sql.Row{
				{int64(1), "first row"},
				{int64(2), "second row"},
			},
		},
		{
			Query:    "SELECT i FROM (SELECT i FROM mytable ORDER BY i LIMIT 2) t",
			Expected: []sql.Row{{int64(1)}},
		},
		// TODO: this is broken: the session limit is applying inappropriately to the subquery
		// {
		// 	"SELECT i FROM (SELECT i FROM mytable ORDER BY i DESC) t ORDER BY i LIMIT 2",
		// 	[]sql.Row{{int64(1)}},
		// },
	}

	harness.Setup(setup.MydbData, setup.MytableData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	err := ctx.Session.SetSessionVariable(ctx, "sql_select_limit", int64(1))
	require.NoError(t, err)

	for _, tt := range q {
		TestQueryWithContext(t, ctx, e, tt.Query, tt.Expected, nil, nil)
	}
}

func TestTracing(t *testing.T, harness Harness) {
	require := require.New(t)
	harness.Setup(setup.MydbData, setup.MytableData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	tracer := new(test.MemTracer)

	sql.WithTracer(tracer)(ctx)

	sch, iter, err := e.Query(ctx, `SELECT DISTINCT i
		FROM mytable
		WHERE s = 'first row'
		ORDER BY i DESC
		LIMIT 1`)
	require.NoError(err)

	rows, err := sql.RowIterToRows(ctx, sch, iter)
	require.Len(rows, 1)
	require.NoError(err)

	spans := tracer.Spans
	var expectedSpans = []string{
		"plan.Limit",
		"plan.TopN",
		"plan.Distinct",
		"plan.Project",
		"plan.IndexedTableAccess",
	}

	var spanOperations []string
	for _, s := range spans {
		// only check the ones inside the execution tree
		if strings.HasPrefix(s, "plan.") ||
			strings.HasPrefix(s, "expression.") ||
			strings.HasPrefix(s, "function.") ||
			strings.HasPrefix(s, "aggregation.") {
			spanOperations = append(spanOperations, s)
		}
	}

	require.Equal(expectedSpans, spanOperations)
}

func TestCurrentTimestamp(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	e := mustNewEngine(t, harness)
	defer e.Close()

	date := time.Date(
		2000,      // year
		12,        // month
		12,        // day
		10,        // hour
		15,        // min
		45,        // sec
		987654321, // nsec
		time.UTC,  // location (UTC)
	)

	testCases := []queries.QueryTest{
		{
			Query:    `SELECT CURRENT_TIMESTAMP(0)`,
			Expected: []sql.Row{{time.Date(2000, time.December, 12, 10, 15, 45, 0, time.UTC)}},
		},
		{
			Query:    `SELECT CURRENT_TIMESTAMP(1)`,
			Expected: []sql.Row{{time.Date(2000, time.December, 12, 10, 15, 45, 900000000, time.UTC)}},
		},
		{
			Query:    `SELECT CURRENT_TIMESTAMP(2)`,
			Expected: []sql.Row{{time.Date(2000, time.December, 12, 10, 15, 45, 980000000, time.UTC)}},
		},
		{
			Query:    `SELECT CURRENT_TIMESTAMP(3)`,
			Expected: []sql.Row{{time.Date(2000, time.December, 12, 10, 15, 45, 987000000, time.UTC)}},
		},
		{
			Query:    `SELECT CURRENT_TIMESTAMP(4)`,
			Expected: []sql.Row{{time.Date(2000, time.December, 12, 10, 15, 45, 987600000, time.UTC)}},
		},
		{
			Query:    `SELECT CURRENT_TIMESTAMP(5)`,
			Expected: []sql.Row{{time.Date(2000, time.December, 12, 10, 15, 45, 987650000, time.UTC)}},
		},
		{
			Query:    `SELECT CURRENT_TIMESTAMP(6)`,
			Expected: []sql.Row{{time.Date(2000, time.December, 12, 10, 15, 45, 987654000, time.UTC)}},
		},
	}

	errorTests := []queries.GenericErrorQueryTest{
		{
			Query: "SELECT CURRENT_TIMESTAMP(-1)",
		},
		{
			Query: `SELECT CURRENT_TIMESTAMP(NULL)`,
		},
		{
			Query: "SELECT CURRENT_TIMESTAMP('notanint')",
		},
	}

	for _, tt := range testCases {
		sql.RunWithNowFunc(func() time.Time {
			return date
		}, func() error {
			TestQuery(t, harness, tt.Query, tt.Expected, tt.ExpectedColumns, tt.Bindings)
			return nil
		})
	}
	for _, tt := range errorTests {
		sql.RunWithNowFunc(func() time.Time {
			return date
		}, func() error {
			runGenericErrorTest(t, harness, tt)
			return nil
		})
	}
}

func TestAddDropPks(t *testing.T, harness Harness) {
	harness.Setup([]setup.SetupScript{{
		"create database mydb",
		"use mydb",
		"create table t1 (pk text, v text, primary key (pk, v))",
		"insert into t1 values ('a1', 'a2'), ('a2', 'a3'), ('a3', 'a4')",
	}})
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	TestQueryWithContext(t, ctx, e, `SELECT * FROM t1`, []sql.Row{
		{"a1", "a2"},
		{"a2", "a3"},
		{"a3", "a4"},
	}, nil, nil)

	RunQuery(t, e, harness, `ALTER TABLE t1 DROP PRIMARY KEY`)

	// Assert the table is still queryable
	TestQueryWithContext(t, ctx, e, `SELECT * FROM t1`, []sql.Row{
		{"a1", "a2"},
		{"a2", "a3"},
		{"a3", "a4"},
	}, nil, nil)

	// Assert that the table is insertable
	TestQueryWithContext(t, ctx, e, `INSERT INTO t1 VALUES ("a1", "a2")`, []sql.Row{
		sql.Row{sql.OkResult{RowsAffected: 1}},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `SELECT * FROM t1 ORDER BY pk`, []sql.Row{
		{"a1", "a2"},
		{"a1", "a2"},
		{"a2", "a3"},
		{"a3", "a4"},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `DELETE FROM t1 WHERE pk = "a1" LIMIT 1`, []sql.Row{
		sql.Row{sql.OkResult{RowsAffected: 1}},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `SELECT * FROM t1 ORDER BY pk`, []sql.Row{
		{"a1", "a2"},
		{"a2", "a3"},
		{"a3", "a4"},
	}, nil, nil)

	// Add back a new primary key and assert the table is queryable
	RunQuery(t, e, harness, `ALTER TABLE t1 ADD PRIMARY KEY (pk, v)`)
	TestQueryWithContext(t, ctx, e, `SELECT * FROM t1`, []sql.Row{
		{"a1", "a2"},
		{"a2", "a3"},
		{"a3", "a4"},
	}, nil, nil)

	// Drop the original Pk, create an index, create a new primary key
	RunQuery(t, e, harness, `ALTER TABLE t1 DROP PRIMARY KEY`)
	RunQuery(t, e, harness, `ALTER TABLE t1 ADD INDEX myidx (v)`)
	RunQuery(t, e, harness, `ALTER TABLE t1 ADD PRIMARY KEY (pk)`)

	// Assert the table is insertable
	TestQueryWithContext(t, ctx, e, `INSERT INTO t1 VALUES ("a4", "a3")`, []sql.Row{
		sql.Row{sql.OkResult{RowsAffected: 1}},
	}, nil, nil)

	// Assert that an indexed based query still functions appropriately
	TestQueryWithContext(t, ctx, e, `SELECT * FROM t1 WHERE v='a3'`, []sql.Row{
		{"a2", "a3"},
		{"a4", "a3"},
	}, nil, nil)

	RunQuery(t, e, harness, `ALTER TABLE t1 DROP PRIMARY KEY`)

	// Assert that the table is insertable
	TestQueryWithContext(t, ctx, e, `INSERT INTO t1 VALUES ("a1", "a2")`, []sql.Row{
		sql.Row{sql.OkResult{RowsAffected: 1}},
	}, nil, nil)

	TestQueryWithContext(t, ctx, e, `SELECT * FROM t1 ORDER BY pk`, []sql.Row{
		{"a1", "a2"},
		{"a1", "a2"},
		{"a2", "a3"},
		{"a3", "a4"},
		{"a4", "a3"},
	}, nil, nil)

	// Assert that a duplicate row causes an alter table error
	AssertErr(t, e, harness, `ALTER TABLE t1 ADD PRIMARY KEY (pk, v)`, sql.ErrPrimaryKeyViolation)

	// Assert that the schema of t1 is unchanged
	TestQueryWithContext(t, ctx, e, `DESCRIBE t1`, []sql.Row{
		{"pk", "text", "NO", "", "", ""},
		{"v", "text", "NO", "MUL", "", ""},
	}, nil, nil)

	// Assert that adding a primary key with an unknown column causes an error
	AssertErr(t, e, harness, `ALTER TABLE t1 ADD PRIMARY KEY (v2)`, sql.ErrKeyColumnDoesNotExist)

	// Truncate the table and re-add rows
	RunQuery(t, e, harness, "TRUNCATE t1")
	RunQuery(t, e, harness, "ALTER TABLE t1 DROP INDEX myidx")
	RunQuery(t, e, harness, `ALTER TABLE t1 ADD PRIMARY KEY (pk, v)`)
	RunQuery(t, e, harness, `INSERT INTO t1 values ("a1","a2"),("a2","a3"),("a3","a4")`)

	// Execute a MultiDDL Alter Statement
	RunQuery(t, e, harness, `ALTER TABLE t1 DROP PRIMARY KEY, ADD PRIMARY KEY (v)`)
	TestQueryWithContext(t, ctx, e, `DESCRIBE t1`, []sql.Row{
		{"pk", "text", "NO", "", "", ""},
		{"v", "text", "NO", "PRI", "", ""},
	}, nil, nil)
	AssertErr(t, e, harness, `INSERT INTO t1 (pk, v) values ("a100", "a3")`, sql.ErrPrimaryKeyViolation)

	TestQueryWithContext(t, ctx, e, `SELECT * FROM t1 ORDER BY pk`, []sql.Row{
		{"a1", "a2"},
		{"a2", "a3"},
		{"a3", "a4"},
	}, nil, nil)
	RunQuery(t, e, harness, `ALTER TABLE t1 DROP PRIMARY KEY`)

	// Technically the query beneath errors in MySQL but I'm pretty sure it's a bug cc:
	// https://stackoverflow.com/questions/8301744/mysql-reports-a-primary-key-but-can-not-drop-it-from-the-table
	RunQuery(t, e, harness, `ALTER TABLE t1 ADD PRIMARY KEY (pk, v), DROP PRIMARY KEY`)
	TestQueryWithContext(t, ctx, e, `DESCRIBE t1`, []sql.Row{
		{"pk", "text", "NO", "", "", ""},
		{"v", "text", "NO", "", "", ""},
	}, nil, nil)
	TestQueryWithContext(t, ctx, e, `SELECT * FROM t1 ORDER BY pk`, []sql.Row{
		{"a1", "a2"},
		{"a2", "a3"},
		{"a3", "a4"},
	}, nil, nil)

	t.Run("No database selected", func(t *testing.T) {
		// Create new database and table and alter the table in other database
		RunQuery(t, e, harness, `CREATE DATABASE newdb`)
		RunQuery(t, e, harness, `CREATE TABLE newdb.tab1 (pk int, c1 int)`)
		RunQuery(t, e, harness, `ALTER TABLE newdb.tab1 ADD PRIMARY KEY (pk)`)

		// Assert that the pk is not primary key
		TestQueryWithContext(t, ctx, e, `SHOW CREATE TABLE newdb.tab1`, []sql.Row{
			{"tab1", "CREATE TABLE `tab1` (\n  `pk` int NOT NULL,\n  `c1` int,\n  PRIMARY KEY (`pk`)\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin"},
		}, nil, nil)

		// Drop all primary key from other database table
		RunQuery(t, e, harness, `ALTER TABLE newdb.tab1 DROP PRIMARY KEY`)

		// Assert that NOT NULL constraint is kept
		TestQueryWithContext(t, ctx, e, `SHOW CREATE TABLE newdb.tab1`, []sql.Row{
			{"tab1", "CREATE TABLE `tab1` (\n  `pk` int NOT NULL,\n  `c1` int\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin"},
		}, nil, nil)
	})
}

func TestNullRanges(t *testing.T, harness Harness) {
	harness.Setup(setup.NullsSetup...)
	for _, tt := range queries.NullRangeTests {
		TestQuery(t, harness, tt.Query, tt.Expected, nil, nil)
	}
}

// RunQuery runs the query given and asserts that it doesn't result in an error.
func RunQuery(t *testing.T, e *sqle.Engine, harness Harness, query string) {
	ctx := NewContext(harness)
	RunQueryWithContext(t, e, ctx, query)
}

// RunQueryWithContext runs the query given and asserts that it doesn't result in an error.
func RunQueryWithContext(t *testing.T, e *sqle.Engine, ctx *sql.Context, query string) {
	ctx = ctx.WithQuery(query)
	sch, iter, err := e.Query(ctx, query)
	require.NoError(t, err)
	_, err = sql.RowIterToRows(ctx, sch, iter)
	require.NoError(t, err)
}

// AssertErr asserts that the given query returns an error during its execution, optionally specifying a type of error.
func AssertErr(t *testing.T, e *sqle.Engine, harness Harness, query string, expectedErrKind *errors.Kind, errStrs ...string) {
	AssertErrWithCtx(t, e, NewContext(harness), query, expectedErrKind, errStrs...)
}

// AssertErrWithBindings asserts that the given query returns an error during its execution, optionally specifying a
// type of error.
func AssertErrWithBindings(t *testing.T, e *sqle.Engine, harness Harness, query string, bindings map[string]sql.Expression, expectedErrKind *errors.Kind, errStrs ...string) {
	ctx := NewContext(harness)
	sch, iter, err := e.QueryWithBindings(ctx, query, bindings)
	if err == nil {
		_, err = sql.RowIterToRows(ctx, sch, iter)
	}
	require.Error(t, err)
	if expectedErrKind != nil {
		require.True(t, expectedErrKind.Is(err), "Expected error of type %s but got %s", expectedErrKind, err)
	} else if len(errStrs) >= 1 {
		require.Equal(t, errStrs[0], err.Error())
	}

}

// AssertErrWithCtx is the same as AssertErr, but uses the context given instead of creating one from a harness
func AssertErrWithCtx(t *testing.T, e *sqle.Engine, ctx *sql.Context, query string, expectedErrKind *errors.Kind, errStrs ...string) {
	ctx = ctx.WithQuery(query)
	sch, iter, err := e.Query(ctx, query)
	if err == nil {
		_, err = sql.RowIterToRows(ctx, sch, iter)
	}
	require.Error(t, err)
	if expectedErrKind != nil {
		_, orig, _ := sql.CastSQLError(err)
		require.True(t, expectedErrKind.Is(orig), "Expected error of type %s but got %s", expectedErrKind, err)
	}
	// If there are multiple error strings then we only match against the first
	if len(errStrs) >= 1 {
		require.Equal(t, errStrs[0], err.Error())
	}
}

// AssertWarningAndTestQuery tests the query and asserts an expected warning code. If |ctx| is provided, it will be
// used. Otherwise the harness will be used to create a fresh context.
func AssertWarningAndTestQuery(
	t *testing.T,
	e *sqle.Engine,
	ctx *sql.Context,
	harness Harness,
	query string,
	expected []sql.Row,
	expectedCols []*sql.Column,
	expectedCode int,
	expectedWarningsCount int,
	expectedWarningMessageSubstring string,
	skipResultsCheck bool,
) {
	require := require.New(t)
	if ctx == nil {
		ctx = NewContext(harness)
	}
	ctx.ClearWarnings()
	ctx = ctx.WithQuery(query)

	sch, iter, err := e.Query(ctx, query)
	require.NoError(err, "Unexpected error for query %s", query)

	rows, err := sql.RowIterToRows(ctx, sch, iter)
	require.NoError(err, "Unexpected error for query %s", query)

	if expectedWarningsCount > 0 {
		assert.Equal(t, expectedWarningsCount, len(ctx.Warnings()))
	}

	if expectedCode > 0 {
		for _, warning := range ctx.Warnings() {
			assert.Equal(t, expectedCode, warning.Code, "Unexpected warning code")
		}
	}

	if len(expectedWarningMessageSubstring) > 0 {
		for _, warning := range ctx.Warnings() {
			assert.Contains(t, warning.Message, expectedWarningMessageSubstring, "Unexpected warning message")
		}
	}

	if !skipResultsCheck {
		checkResults(t, require, expected, expectedCols, sch, rows, query)
	}
}

type customFunc struct {
	expression.UnaryExpression
}

func (c customFunc) String() string {
	return "customFunc(" + c.Child.String() + ")"
}

func (c customFunc) Type() sql.Type {
	return sql.Uint32
}

func (c customFunc) Eval(ctx *sql.Context, row sql.Row) (interface{}, error) {
	return int64(5), nil
}

func (c customFunc) WithChildren(children ...sql.Expression) (sql.Expression, error) {
	return &customFunc{expression.UnaryExpression{children[0]}}, nil
}

func TestAlterTable(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData)
	e := mustNewEngine(t, harness)
	defer e.Close()

	t.Run("Modify column invalid after", func(t *testing.T) {
		RunQuery(t, e, harness, "CREATE TABLE t1008(pk BIGINT DEFAULT (v2) PRIMARY KEY, v1 BIGINT DEFAULT (pk), v2 BIGINT)")
		AssertErr(t, e, harness, "ALTER TABLE t1008 MODIFY COLUMN v1 BIGINT DEFAULT (pk) AFTER v3", sql.ErrTableColumnNotFound)
	})

	t.Run("Add column invalid after", func(t *testing.T) {
		RunQuery(t, e, harness, "CREATE TABLE t1009(pk BIGINT DEFAULT (v2) PRIMARY KEY, v1 BIGINT DEFAULT (pk), v2 BIGINT)")
		AssertErr(t, e, harness, "ALTER TABLE t1009 ADD COLUMN v4 BIGINT DEFAULT (pk) AFTER v3", sql.ErrTableColumnNotFound)
	})

	t.Run("rename column added in same statement", func(t *testing.T) {
		RunQuery(t, e, harness, "CREATE TABLE t30(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT '4')")
		AssertErr(t, e, harness, "ALTER TABLE t30 ADD COLUMN v3 BIGINT DEFAULT 5, RENAME COLUMN v3 to v2", sql.ErrTableColumnNotFound)
	})

	t.Run("modify column added in same statement", func(t *testing.T) {
		RunQuery(t, e, harness, "CREATE TABLE t31(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT '4')")
		AssertErr(t, e, harness, "ALTER TABLE t31 ADD COLUMN v3 BIGINT DEFAULT 5, modify column v3 bigint default null", sql.ErrTableColumnNotFound)
	})

	t.Run("variety of alter column statements in a single statement", func(t *testing.T) {
		RunQuery(t, e, harness, "CREATE TABLE t32(pk BIGINT PRIMARY KEY, v1 int, v2 int, v3 int, toRename int)")
		RunQuery(t, e, harness, `alter table t32 add column v4 int after pk,
			drop column v2, modify v1 varchar(100) not null,
			alter column v3 set default 100, rename column toRename to newName`)

		ctx := NewContext(harness)
		t32, _, err := e.Analyzer.Catalog.Table(ctx, ctx.GetCurrentDatabase(), "t32")
		require.NoError(t, err)
		assertSchemasEqualWithDefaults(t, sql.Schema{
			{Name: "pk", Type: sql.Int64, Nullable: false, Source: "t32", PrimaryKey: true},
			{Name: "v4", Type: sql.Int32, Nullable: true, Source: "t32"},
			{Name: "v1", Type: sql.MustCreateStringWithDefaults(sqltypes.VarChar, 100), Source: "t32"},
			{Name: "v3", Type: sql.Int32, Nullable: true, Source: "t32", Default: NewColumnDefaultValue(expression.NewLiteral(int8(100), sql.Int8), sql.Int32, true, true)},
			{Name: "newName", Type: sql.Int32, Nullable: true, Source: "t32"},
		}, t32.Schema())

		RunQuery(t, e, harness, "CREATE TABLE t32_2(pk BIGINT PRIMARY KEY, v1 int, v2 int, v3 int)")
		RunQuery(t, e, harness, `alter table t32_2 drop v1, add v1 int`)

		t32, _, err = e.Analyzer.Catalog.Table(ctx, ctx.GetCurrentDatabase(), "t32_2")
		require.NoError(t, err)
		assertSchemasEqualWithDefaults(t, sql.Schema{
			{Name: "pk", Type: sql.Int64, Nullable: false, Source: "t32_2", PrimaryKey: true},
			{Name: "v2", Type: sql.Int32, Nullable: true, Source: "t32_2"},
			{Name: "v3", Type: sql.Int32, Nullable: true, Source: "t32_2"},
			{Name: "v1", Type: sql.Int32, Nullable: true, Source: "t32_2"},
		}, t32.Schema())

		RunQuery(t, e, harness, "CREATE TABLE t32_3(pk BIGINT PRIMARY KEY, v1 int, v2 int, v3 int)")
		RunQuery(t, e, harness, `alter table t32_3 rename column v1 to v5, add v1 int`)

		t32, _, err = e.Analyzer.Catalog.Table(ctx, ctx.GetCurrentDatabase(), "t32_3")
		require.NoError(t, err)
		assertSchemasEqualWithDefaults(t, sql.Schema{
			{Name: "pk", Type: sql.Int64, Nullable: false, Source: "t32_3", PrimaryKey: true},
			{Name: "v5", Type: sql.Int32, Nullable: true, Source: "t32_3"},
			{Name: "v2", Type: sql.Int32, Nullable: true, Source: "t32_3"},
			{Name: "v3", Type: sql.Int32, Nullable: true, Source: "t32_3"},
			{Name: "v1", Type: sql.Int32, Nullable: true, Source: "t32_3"},
		}, t32.Schema())

		// Error cases: dropping a column added in the same statement, dropping a column not present in the original schema,
		// dropping a column renamed away
		AssertErr(t, e, harness, "alter table t32 add column vnew int, drop column vnew", sql.ErrTableColumnNotFound)
		AssertErr(t, e, harness, "alter table t32 rename column v3 to v5, drop column v5", sql.ErrTableColumnNotFound)
		AssertErr(t, e, harness, "alter table t32 rename column v3 to v5, drop column v3", sql.ErrTableColumnNotFound)
	})

	t.Run("mix of alter column, add and drop constraints in one statement", func(t *testing.T) {
		RunQuery(t, e, harness, "CREATE TABLE t33(pk BIGINT PRIMARY KEY, v1 int, v2 int)")
		RunQuery(t, e, harness, `alter table t33 add column v4 int after pk, 
			drop column v2, add constraint v1gt0 check (v1 > 0)`)

		ctx := NewContext(harness)
		t33, _, err := e.Analyzer.Catalog.Table(ctx, ctx.GetCurrentDatabase(), "t33")
		require.NoError(t, err)
		assert.Equal(t, sql.Schema{
			{Name: "pk", Type: sql.Int64, Nullable: false, Source: "t33", PrimaryKey: true},
			{Name: "v4", Type: sql.Int32, Nullable: true, Source: "t33"},
			{Name: "v1", Type: sql.Int32, Nullable: true, Source: "t33"},
		}, t33.Schema())

		ct, ok := t33.(sql.CheckTable)
		require.True(t, ok, "CheckTable required for this test")
		checks, err := ct.GetChecks(ctx)
		require.NoError(t, err)
		assert.Equal(t, []sql.CheckDefinition{
			{
				Name:            "v1gt0",
				CheckExpression: "(v1 > 0)",
				Enforced:        true,
			},
		}, checks)
	})

	t.Run("drop column preserves check constraints", func(t *testing.T) {
		RunQuery(t, e, harness, "create table t34 (i bigint primary key, s varchar(20))")
		RunQuery(t, e, harness, "ALTER TABLE t34 ADD COLUMN j int, ADD COLUMN k int")
		RunQuery(t, e, harness, "ALTER TABLE t34 ADD CONSTRAINT test_check CHECK (j < 12345)")

		AssertErr(t, e, harness, "ALTER TABLE t34 DROP COLUMN j", sql.ErrCheckConstraintInvalidatedByColumnAlter)

		RunQuery(t, e, harness, "ALTER TABLE t34 DROP COLUMN k")
		tt := queries.QueryTest{
			Query: "show create table t34",
			Expected: []sql.Row{{"t34", "CREATE TABLE `t34` (\n" +
				"  `i` bigint NOT NULL,\n" +
				"  `s` varchar(20),\n" +
				"  `j` int,\n" +
				"  PRIMARY KEY (`i`),\n" +
				"  CONSTRAINT `test_check` CHECK ((`j` < 12345))\n" +
				") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin"}},
		}
		TestQueryWithEngine(t, harness, e, tt)
	})

	t.Run("drop column preserves indexes", func(t *testing.T) {
		ctx := NewContext(harness)
		RunQuery(t, e, harness, "create table t35 (i bigint primary key, s varchar(20), s2 varchar(20))")
		RunQuery(t, e, harness, "ALTER TABLE t35 ADD unique key test_key (s)")

		RunQuery(t, e, harness, "ALTER TABLE t35 DROP COLUMN s2")
		TestQueryWithContext(t, ctx, e, "show create table t35",
			[]sql.Row{{"t35", "CREATE TABLE `t35` (\n" +
				"  `i` bigint NOT NULL,\n" +
				"  `s` varchar(20),\n" +
				"  PRIMARY KEY (`i`),\n" +
				"  UNIQUE KEY `test_key` (`s`)\n" +
				") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin"}},
			nil, nil)
	})

	t.Run("drop column prevents foreign key violations", func(t *testing.T) {
		RunQuery(t, e, harness, "create table t36 (i bigint primary key, j varchar(20))")
		RunQuery(t, e, harness, "create table t37 (i bigint primary key, j varchar(20))")
		RunQuery(t, e, harness, "ALTER TABLE t36 ADD key (j)")
		RunQuery(t, e, harness, "ALTER TABLE t37 ADD constraint fk_36 foreign key (j) references t36(j)")

		AssertErr(t, e, harness, "ALTER TABLE t37 DROP COLUMN j", sql.ErrForeignKeyDropColumn)
	})

	t.Run("disable keys / enable keys", func(t *testing.T) {
		ctx := NewContext(harness)
		AssertWarningAndTestQuery(t, e, ctx, harness, "ALTER TABLE t33 DISABLE KEYS",
			[]sql.Row{{sql.NewOkResult(0)}},
			nil, mysql.ERNotSupportedYet, 1,
			"", false)
		AssertWarningAndTestQuery(t, e, ctx, harness, "ALTER TABLE t33 ENABLE KEYS",
			[]sql.Row{{sql.NewOkResult(0)}}, nil, mysql.ERNotSupportedYet, 1,
			"", false)
	})
}

func NewColumnDefaultValue(expr sql.Expression, outType sql.Type, representsLiteral bool, mayReturnNil bool) *sql.ColumnDefaultValue {
	cdv, err := sql.NewColumnDefaultValue(expr, outType, representsLiteral, mayReturnNil)
	if err != nil {
		panic(err)
	}
	return cdv
}

func TestColumnDefaults(t *testing.T, harness Harness) {
	harness.Setup(setup.MydbData, setup.MytableData)
	e := mustNewEngine(t, harness)
	defer e.Close()
	ctx := NewContext(harness)

	e.Analyzer.Catalog.RegisterFunction(NewContext(harness), sql.Function1{
		Name: "customfunc",
		Fn: func(e1 sql.Expression) sql.Expression {
			return &customFunc{expression.UnaryExpression{e1}}
		},
	})

	t.Run("Standard default literal", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t1(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT 2)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t1 (pk) VALUES (1), (2)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t1", []sql.Row{{1, 2}, {2, 2}}, nil, nil)
	})

	t.Run("Default expression with function and referenced column", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t2(pk BIGINT PRIMARY KEY, v1 SMALLINT DEFAULT (GREATEST(pk, 2)))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t2 (pk) VALUES (1), (2), (3)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t2", []sql.Row{{1, 2}, {2, 2}, {3, 3}}, nil, nil)
	})

	t.Run("Default expression converting to proper column type", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t3(pk BIGINT PRIMARY KEY, v1 VARCHAR(20) DEFAULT (GREATEST(pk, 2)))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t3 (pk) VALUES (1), (2), (3)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t3", []sql.Row{{1, "2"}, {2, "2"}, {3, "3"}}, nil, nil)
	})

	t.Run("Default literal of different type but implicitly converts", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t4(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT '4')", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t4 (pk) VALUES (1), (2)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t4", []sql.Row{{1, 4}, {2, 4}}, nil, nil)
	})

	t.Run("Back reference to default literal", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t5(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT (v2), v2 BIGINT DEFAULT 7)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t5 (pk) VALUES (1), (2)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t5", []sql.Row{{1, 7, 7}, {2, 7, 7}}, nil, nil)
	})

	t.Run("Forward reference to default literal", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t6(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT 9, v2 BIGINT DEFAULT (v1))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t6 (pk) VALUES (1), (2)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t6", []sql.Row{{1, 9, 9}, {2, 9, 9}}, nil, nil)
	})

	t.Run("Forward reference to default expression", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t7(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT (8), v2 BIGINT DEFAULT (v1))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t7 (pk) VALUES (1), (2)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t7", []sql.Row{{1, 8, 8}, {2, 8, 8}}, nil, nil)
	})

	t.Run("Back reference to value", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t8(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT (v2 + 1), v2 BIGINT)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t8 (pk, v2) VALUES (1, 4), (2, 6)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t8", []sql.Row{{1, 5, 4}, {2, 7, 6}}, nil, nil)
	})

	t.Run("TEXT expression", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t9(pk BIGINT PRIMARY KEY, v1 LONGTEXT DEFAULT (77))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t9 (pk) VALUES (1), (2)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t9", []sql.Row{{1, "77"}, {2, "77"}}, nil, nil)
	})

	t.Run("DATETIME/TIMESTAMP NOW/CURRENT_TIMESTAMP current_timestamp", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t10(pk BIGINT PRIMARY KEY, v1 DATETIME DEFAULT NOW(), v2 DATETIME DEFAULT CURRENT_TIMESTAMP(),"+
			"v3 TIMESTAMP DEFAULT NOW(), v4 TIMESTAMP DEFAULT CURRENT_TIMESTAMP())", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		// truncating time to microseconds for compatibility with integrators who may store more precision (go gives nanos)
		now := time.Now().Truncate(time.Microsecond)
		sql.RunWithNowFunc(func() time.Time {
			return now
		}, func() error {
			RunQuery(t, e, harness, "insert into t10(pk) values (1)")
			return nil
		})
		TestQueryWithContext(t, ctx, e, "select * from t10 order by 1", []sql.Row{
			{1, now.UTC(), now.UTC().Truncate(time.Second), now.UTC(), now.UTC().Truncate(time.Second)},
		}, nil, nil)
	})

	// TODO: zero timestamps work slightly differently than they do in MySQL, where the zero time is "0000-00-00 00:00:00"
	//  We use "0000-01-01 00:00:00"
	t.Run("DATETIME/TIMESTAMP NOW/CURRENT_TIMESTAMP literals", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t10zero(pk BIGINT PRIMARY KEY, v1 DATETIME DEFAULT '2020-01-01 01:02:03', v2 DATETIME DEFAULT 0,"+
			"v3 TIMESTAMP DEFAULT '2020-01-01 01:02:03', v4 TIMESTAMP DEFAULT 0)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		RunQuery(t, e, harness, "insert into t10zero(pk) values (1)")

		// TODO: the string conversion does not transform to UTC like other NOW() calls, fix this
		TestQueryWithContext(t, ctx, e, "select * from t10zero order by 1", []sql.Row{{1, time.Date(2020, 1, 1, 1, 2, 3, 0, time.UTC), sql.Datetime.Zero(), time.Date(2020, 1, 1, 1, 2, 3, 0, time.UTC), sql.Timestamp.Zero()}}, nil, nil)
	})

	t.Run("Non-DATETIME/TIMESTAMP NOW/CURRENT_TIMESTAMP expression", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t11(pk BIGINT PRIMARY KEY, v1 DATE DEFAULT (NOW()), v2 VARCHAR(20) DEFAULT (CURRENT_TIMESTAMP()))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		now := time.Now()
		sql.RunWithNowFunc(func() time.Time {
			return now
		}, func() error {
			RunQuery(t, e, harness, "insert into t11(pk) values (1)")
			return nil
		})

		// TODO: the string conversion does not transform to UTC like other NOW() calls, fix this
		TestQueryWithContext(t, ctx, e, "select * from t11 order by 1", []sql.Row{{1, now.UTC().Truncate(time.Hour * 24), now.Truncate(time.Second).Format(sql.TimestampDatetimeLayout)}}, nil, nil)
	})

	t.Run("REPLACE INTO with default expression", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t12(pk BIGINT PRIMARY KEY, v1 SMALLINT DEFAULT (GREATEST(pk, 2)))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t12 (pk) VALUES (1), (2)")
		RunQuery(t, e, harness, "REPLACE INTO t12 (pk) VALUES (2), (3)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t12", []sql.Row{{1, 2}, {2, 2}, {3, 3}}, nil, nil)
	})

	t.Run("Add column last default literal", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t13(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT '4')", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t13 (pk) VALUES (1), (2)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t13 ADD COLUMN v2 BIGINT DEFAULT 5", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t13", []sql.Row{{1, 4, 5}, {2, 4, 5}}, nil, nil)
	})

	t.Run("Add column implicit last default expression", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t14(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT (pk + 1))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t14 (pk) VALUES (1), (2)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t14 ADD COLUMN v2 BIGINT DEFAULT (v1 + 2)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t14", []sql.Row{{1, 2, 4}, {2, 3, 5}}, nil, nil)
	})

	t.Run("Add column explicit last default expression", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t15(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT (pk + 1))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t15 (pk) VALUES (1), (2)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t15 ADD COLUMN v2 BIGINT DEFAULT (v1 + 2) AFTER v1", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t15", []sql.Row{{1, 2, 4}, {2, 3, 5}}, nil, nil)
	})

	t.Run("Add column first default literal", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t16(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT '4')", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t16 (pk) VALUES (1), (2)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t16 ADD COLUMN v2 BIGINT DEFAULT 5 FIRST", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t16", []sql.Row{{5, 1, 4}, {5, 2, 4}}, nil, nil)
	})

	t.Run("Add column first default expression", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t17(pk BIGINT PRIMARY KEY, v1 BIGINT)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t17 VALUES (1, 3), (2, 4)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t17 ADD COLUMN v2 BIGINT DEFAULT (v1 + 2) FIRST", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t17", []sql.Row{{5, 1, 3}, {6, 2, 4}}, nil, nil)
	})

	t.Run("Add column forward reference to default expression", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t18(pk BIGINT DEFAULT (v1) PRIMARY KEY, v1 BIGINT)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t18 (v1) VALUES (1), (2)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t18 ADD COLUMN v2 BIGINT DEFAULT (pk + 1) AFTER pk", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t18", []sql.Row{{1, 2, 1}, {2, 3, 2}}, nil, nil)
	})

	t.Run("Add column back reference to default literal", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t19(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT 5)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t19 (pk) VALUES (1), (2)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t19 ADD COLUMN v2 BIGINT DEFAULT (v1 - 1) AFTER pk", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t19", []sql.Row{{1, 4, 5}, {2, 4, 5}}, nil, nil)
	})

	t.Run("Add column first with existing defaults still functioning", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t20(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT (pk + 10))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t20 (pk) VALUES (1), (2)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t20 ADD COLUMN v2 BIGINT DEFAULT (-pk) FIRST", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t20 (pk) VALUES (3)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t20", []sql.Row{{-1, 1, 11}, {-2, 2, 12}, {-3, 3, 13}}, nil, nil)
	})

	t.Run("Drop column referencing other column", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t21(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT (v2), v2 BIGINT)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t21 DROP COLUMN v1", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
	})

	t.Run("Modify column move first forward reference default literal", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t22(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT (pk + 2), v2 BIGINT DEFAULT (pk + 1))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t22 (pk) VALUES (1), (2)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t22 MODIFY COLUMN v1 BIGINT DEFAULT (pk + 2) FIRST", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t22", []sql.Row{{3, 1, 2}, {4, 2, 3}}, nil, nil)
	})

	t.Run("Modify column move first add reference", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t23(pk BIGINT PRIMARY KEY, v1 BIGINT, v2 BIGINT DEFAULT (v1 + 1))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t23 (pk, v1) VALUES (1, 2), (2, 3)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t23 MODIFY COLUMN v1 BIGINT DEFAULT (pk + 5) FIRST", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t23 (pk) VALUES (3)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t23 order by 1", []sql.Row{
			{2, 1, 3},
			{3, 2, 4},
			{8, 3, 9},
		}, nil, nil)
	})

	t.Run("Modify column move last being referenced", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t24(pk BIGINT PRIMARY KEY, v1 BIGINT, v2 BIGINT DEFAULT (v1 + 1))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t24 (pk, v1) VALUES (1, 2), (2, 3)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t24 MODIFY COLUMN v1 BIGINT AFTER v2", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t24 (pk, v1) VALUES (3, 4)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t24 order by 1", []sql.Row{
			{1, 3, 2},
			{2, 4, 3},
			{3, 5, 4},
		}, nil, nil)
	})

	t.Run("Modify column move last add reference", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t25(pk BIGINT PRIMARY KEY, v1 BIGINT, v2 BIGINT DEFAULT (pk * 2))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t25 (pk, v1) VALUES (1, 2), (2, 3)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t25 MODIFY COLUMN v1 BIGINT DEFAULT (-pk) AFTER v2", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t25 (pk) VALUES (3)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t25", []sql.Row{{1, 2, 2}, {2, 4, 3}, {3, 6, -3}}, nil, nil)
	})

	t.Run("Modify column no move add reference", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t26(pk BIGINT PRIMARY KEY, v1 BIGINT, v2 BIGINT DEFAULT (pk * 2))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t26 (pk, v1) VALUES (1, 2), (2, 3)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t26 MODIFY COLUMN v1 BIGINT DEFAULT (-pk)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t26 (pk) VALUES (3)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t26", []sql.Row{{1, 2, 2}, {2, 3, 4}, {3, -3, 6}}, nil, nil)
	})

	t.Run("Negative float literal", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t27(pk BIGINT PRIMARY KEY, v1 DOUBLE DEFAULT -1.1)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "DESCRIBE t27", []sql.Row{{"pk", "bigint", "NO", "PRI", "", ""}, {"v1", "double", "YES", "", "-1.1", ""}}, nil, nil)
	})

	t.Run("Table referenced with column", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t28(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT (t28.pk))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		RunQuery(t, e, harness, "INSERT INTO t28 (pk) VALUES (1), (2)")
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t28", []sql.Row{{1, 1}, {2, 2}}, nil, nil)

		ctx := NewContext(harness)
		t28, _, err := e.Analyzer.Catalog.Table(ctx, ctx.GetCurrentDatabase(), "t28")
		require.NoError(t, err)
		sch := t28.Schema()
		require.Len(t, sch, 2)
		require.Equal(t, "v1", sch[1].Name)
		require.NotContains(t, sch[1].Default.String(), "t28")
	})

	t.Run("Column referenced with name change", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t29(pk BIGINT PRIMARY KEY, v1 BIGINT, v2 BIGINT DEFAULT (v1 + 1))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)

		RunQuery(t, e, harness, "INSERT INTO t29 (pk, v1) VALUES (1, 2)")
		RunQuery(t, e, harness, "ALTER TABLE t29 RENAME COLUMN v1 to v1x")
		RunQuery(t, e, harness, "INSERT INTO t29 (pk, v1x) VALUES (2, 3)")
		RunQuery(t, e, harness, "ALTER TABLE t29 CHANGE COLUMN v1x v1y BIGINT")
		RunQuery(t, e, harness, "INSERT INTO t29 (pk, v1y) VALUES (3, 4)")

		TestQueryWithContext(t, ctx, e, "SELECT * FROM t29 ORDER BY 1", []sql.Row{{1, 2, 3}, {2, 3, 4}, {3, 4, 5}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SHOW CREATE TABLE t29", []sql.Row{{"t29", "CREATE TABLE `t29` (\n" +
			"  `pk` bigint NOT NULL,\n" +
			"  `v1y` bigint,\n" +
			"  `v2` bigint DEFAULT ((v1y + 1)),\n" +
			"  PRIMARY KEY (`pk`)\n" +
			") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin"}}, nil, nil)
	})

	t.Run("Add multiple columns same ALTER", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t30(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT '4')", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t30 (pk) VALUES (1), (2)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t30 ADD COLUMN v2 BIGINT DEFAULT 5, ADD COLUMN V3 BIGINT DEFAULT 7", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT pk, v1, v2, V3 FROM t30", []sql.Row{{1, 4, 5, 7}, {2, 4, 5, 7}}, nil, nil)
	})

	t.Run("Add non-nullable column without default #1", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t31 (pk BIGINT PRIMARY KEY)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t31 VALUES (1), (2), (3)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t31 ADD COLUMN v1 BIGINT NOT NULL", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t31", []sql.Row{{1, 0}, {2, 0}, {3, 0}}, nil, nil)
	})

	t.Run("Add non-nullable column without default #2", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t32 (pk BIGINT PRIMARY KEY)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		RunQuery(t, e, harness, "INSERT INTO t32 VALUES (1), (2), (3)")
		TestQueryWithContext(t, ctx, e, "ALTER TABLE t32 ADD COLUMN v1 VARCHAR(20) NOT NULL", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "SELECT * FROM t32", []sql.Row{{1, ""}, {2, ""}, {3, ""}}, nil, nil)
	})

	t.Run("Column defaults with functions", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t33(pk varchar(100) DEFAULT (replace(UUID(), '-', '')), v1 timestamp DEFAULT now(), v2 varchar(100), primary key (pk))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "insert into t33 (v2) values ('abc')", []sql.Row{{sql.NewOkResult(1)}}, nil, nil)
		TestQueryWithContext(t, ctx, e, "select count(*) from t33", []sql.Row{{1}}, nil, nil)
		RunQuery(t, e, harness, "alter table t33 add column name varchar(100)")
		RunQuery(t, e, harness, "alter table t33 rename column v1 to v1_new")
		RunQuery(t, e, harness, "alter table t33 rename column name to name2")
		RunQuery(t, e, harness, "alter table t33 drop column name2")

		TestQueryWithContext(t, ctx, e, "desc t33", []sql.Row{
			{"pk", "varchar(100)", "NO", "PRI", "(replace(UUID(), \"-\", \"\"))", ""},
			{"v1_new", "timestamp", "YES", "", "NOW()", ""},
			{"v2", "varchar(100)", "YES", "", "", ""},
		}, nil, nil)
	})

	t.Run("Invalid literal for column type", func(t *testing.T) {
		AssertErr(t, e, harness, "CREATE TABLE t999(pk BIGINT PRIMARY KEY, v1 INT UNSIGNED DEFAULT -1)", sql.ErrIncompatibleDefaultType)
	})

	t.Run("Invalid literal for column type", func(t *testing.T) {
		AssertErr(t, e, harness, "CREATE TABLE t999(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT 'hi')", sql.ErrIncompatibleDefaultType)
	})

	t.Run("Expression contains invalid literal once implicitly converted", func(t *testing.T) {
		AssertErr(t, e, harness, "CREATE TABLE t999(pk BIGINT PRIMARY KEY, v1 INT UNSIGNED DEFAULT '-1')", sql.ErrIncompatibleDefaultType)
	})

	t.Run("Null literal is invalid for NOT NULL", func(t *testing.T) {
		AssertErr(t, e, harness, "CREATE TABLE t999(pk BIGINT PRIMARY KEY, v1 BIGINT NOT NULL DEFAULT NULL)", sql.ErrIncompatibleDefaultType)
	})

	t.Run("Back reference to expression", func(t *testing.T) {
		AssertErr(t, e, harness, "CREATE TABLE t999(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT (v2), v2 BIGINT DEFAULT (9))", sql.ErrInvalidDefaultValueOrder)
	})

	t.Run("TEXT literals", func(t *testing.T) {
		AssertErr(t, e, harness, "CREATE TABLE t999(pk BIGINT PRIMARY KEY, v1 TEXT DEFAULT 'hi')", sql.ErrInvalidTextBlobColumnDefault)
		AssertErr(t, e, harness, "CREATE TABLE t999(pk BIGINT PRIMARY KEY, v1 LONGTEXT DEFAULT 'hi')", sql.ErrInvalidTextBlobColumnDefault)
	})

	t.Run("Other types using NOW/CURRENT_TIMESTAMP literal", func(t *testing.T) {
		AssertErr(t, e, harness, "CREATE TABLE t999(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT NOW())", sql.ErrColumnDefaultDatetimeOnlyFunc)
		AssertErr(t, e, harness, "CREATE TABLE t999(pk BIGINT PRIMARY KEY, v1 VARCHAR(20) DEFAULT CURRENT_TIMESTAMP())", sql.ErrColumnDefaultDatetimeOnlyFunc)
		AssertErr(t, e, harness, "CREATE TABLE t999(pk BIGINT PRIMARY KEY, v1 BIT(5) DEFAULT NOW())", sql.ErrColumnDefaultDatetimeOnlyFunc)
		AssertErr(t, e, harness, "CREATE TABLE t999(pk BIGINT PRIMARY KEY, v1 DATE DEFAULT CURRENT_TIMESTAMP())", sql.ErrColumnDefaultDatetimeOnlyFunc)
	})

	t.Run("Custom functions are invalid", func(t *testing.T) {
		t.Skip("Broken: should produce an error, but does not")
		AssertErr(t, e, harness, "CREATE TABLE t999(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT (CUSTOMFUNC(1)))", sql.ErrInvalidColumnDefaultFunction)
	})

	t.Run("Default expression references own column", func(t *testing.T) {
		AssertErr(t, e, harness, "CREATE TABLE t999(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT (v1))", sql.ErrInvalidDefaultValueOrder)
	})

	t.Run("Expression contains invalid literal, fails on insertion", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t1000(pk BIGINT PRIMARY KEY, v1 INT UNSIGNED DEFAULT (-1))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		AssertErr(t, e, harness, "INSERT INTO t1000 (pk) VALUES (1)", nil)
	})

	t.Run("Expression contains null on NOT NULL, fails on insertion", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t1001(pk BIGINT PRIMARY KEY, v1 BIGINT NOT NULL DEFAULT (NULL))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		AssertErr(t, e, harness, "INSERT INTO t1001 (pk) VALUES (1)", sql.ErrColumnDefaultReturnedNull)
	})

	t.Run("Add column first back reference to expression", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t1002(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT (pk + 1))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		AssertErr(t, e, harness, "ALTER TABLE t1002 ADD COLUMN v2 BIGINT DEFAULT (v1 + 2) FIRST", sql.ErrInvalidDefaultValueOrder)
	})

	t.Run("Add column after back reference to expression", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t1003(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT (pk + 1))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		AssertErr(t, e, harness, "ALTER TABLE t1003 ADD COLUMN v2 BIGINT DEFAULT (v1 + 2) AFTER pk", sql.ErrInvalidDefaultValueOrder)
	})

	t.Run("Add column self reference", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t1004(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT (pk + 1))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		AssertErr(t, e, harness, "ALTER TABLE t1004 ADD COLUMN v2 BIGINT DEFAULT (v2)", sql.ErrInvalidDefaultValueOrder)
	})

	t.Run("Drop column referenced by other column", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t1005(pk BIGINT PRIMARY KEY, v1 BIGINT, v2 BIGINT DEFAULT (v1))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		AssertErr(t, e, harness, "ALTER TABLE t1005 DROP COLUMN v1", sql.ErrDropColumnReferencedInDefault)
	})

	t.Run("Modify column moving back creates back reference to expression", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t1006(pk BIGINT PRIMARY KEY, v1 BIGINT DEFAULT (pk), v2 BIGINT DEFAULT (v1))", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		AssertErr(t, e, harness, "ALTER TABLE t1006 MODIFY COLUMN v1 BIGINT DEFAULT (pk) AFTER v2", sql.ErrInvalidDefaultValueOrder)
	})

	t.Run("Modify column moving forward creates back reference to expression", func(t *testing.T) {
		TestQueryWithContext(t, ctx, e, "CREATE TABLE t1007(pk BIGINT DEFAULT (v2) PRIMARY KEY, v1 BIGINT DEFAULT (pk), v2 BIGINT)", []sql.Row{{sql.NewOkResult(0)}}, nil, nil)
		AssertErr(t, e, harness, "ALTER TABLE t1007 MODIFY COLUMN v1 BIGINT DEFAULT (pk) FIRST", sql.ErrInvalidDefaultValueOrder)
	})
}

func TestPersist(t *testing.T, harness Harness, newPersistableSess func(ctx *sql.Context) sql.PersistableSession) {
	q := []struct {
		Name            string
		Query           string
		Expected        []sql.Row
		ExpectedGlobal  interface{}
		ExpectedPersist interface{}
	}{
		{
			Query:           "SET PERSIST max_connections = 1000;",
			Expected:        []sql.Row{{}},
			ExpectedGlobal:  int64(1000),
			ExpectedPersist: int64(1000),
		}, {
			Query:           "SET @@PERSIST.max_connections = 1000;",
			Expected:        []sql.Row{{}},
			ExpectedGlobal:  int64(1000),
			ExpectedPersist: int64(1000),
		}, {
			Query:           "SET PERSIST_ONLY max_connections = 1000;",
			Expected:        []sql.Row{{}},
			ExpectedGlobal:  int64(151),
			ExpectedPersist: int64(1000),
		},
	}

	harness.Setup(setup.MydbData, setup.MytableData)
	e := mustNewEngine(t, harness)
	defer e.Close()

	for _, tt := range q {
		t.Run(tt.Name, func(t *testing.T) {
			sql.InitSystemVariables()
			ctx := NewContext(harness)
			ctx.Session = newPersistableSess(ctx)

			TestQueryWithContext(t, ctx, e, tt.Query, tt.Expected, nil, nil)

			if tt.ExpectedGlobal != nil {
				_, res, _ := sql.SystemVariables.GetGlobal("max_connections")
				require.Equal(t, tt.ExpectedGlobal, res)
			}

			if tt.ExpectedGlobal != nil {
				res, err := ctx.Session.(sql.PersistableSession).GetPersistedValue("max_connections")
				require.NoError(t, err)
				assert.Equal(t,
					tt.ExpectedPersist, res)
			}
		})
	}
}

func TestKeylessUniqueIndex(t *testing.T, harness Harness) {
	harness.Setup(setup.KeylessSetup...)
	for _, tt := range queries.InsertIntoKeylessUnique {
		runWriteQueryTest(t, harness, tt)
	}

	for _, tt := range queries.InsertIntoKeylessUniqueError {
		runGenericErrorTest(t, harness, tt)
	}
}

func TestPrepared(t *testing.T, harness Harness) {
	qtests := []queries.QueryTest{
		{
			Query: "SELECT i, 1 AS foo, 2 AS bar FROM (SELECT i FROM mYtABLE WHERE i = ?) AS a ORDER BY foo, i",
			Expected: []sql.Row{
				{2, 1, 2}},
			Bindings: map[string]sql.Expression{
				"v1": expression.NewLiteral(int64(2), sql.Int64),
			},
		},
		{
			Query: "SELECT i, 1 AS foo, 2 AS bar FROM (SELECT i FROM mYtABLE WHERE i = :var) AS a WHERE bar = :var ORDER BY foo, i",
			Expected: []sql.Row{
				{2, 1, 2}},
			Bindings: map[string]sql.Expression{
				"var": expression.NewLiteral(int64(2), sql.Int64),
			},
		},
		{
			Query:    "SELECT i, 1 AS foo, 2 AS bar FROM MyTable WHERE bar = ? ORDER BY foo, i;",
			Expected: []sql.Row{},
			Bindings: map[string]sql.Expression{
				"v1": expression.NewLiteral(int64(1), sql.Int64),
			},
		},
		{
			Query:    "SELECT i, 1 AS foo, 2 AS bar FROM MyTable WHERE bar = :bar AND foo = :foo ORDER BY foo, i;",
			Expected: []sql.Row{},
			Bindings: map[string]sql.Expression{
				"bar": expression.NewLiteral(int64(1), sql.Int64),
				"foo": expression.NewLiteral(int64(1), sql.Int64),
			},
		},
		{
			Query: "SELECT :foo * 2",
			Expected: []sql.Row{
				{2},
			},
			Bindings: map[string]sql.Expression{
				"foo": expression.NewLiteral(int64(1), sql.Int64),
			},
		},
		{
			Query: "SELECT i from mytable where i in (:foo, :bar) order by 1",
			Expected: []sql.Row{
				{1},
				{2},
			},
			Bindings: map[string]sql.Expression{
				"foo": expression.NewLiteral(int64(1), sql.Int64),
				"bar": expression.NewLiteral(int64(2), sql.Int64),
			},
		},
		{
			Query: "SELECT i from mytable where i = :foo * 2",
			Expected: []sql.Row{
				{2},
			},
			Bindings: map[string]sql.Expression{
				"foo": expression.NewLiteral(int64(1), sql.Int64),
			},
		},
		{
			Query: "SELECT i from mytable where 4 = :foo * 2 order by 1",
			Expected: []sql.Row{
				{1},
				{2},
				{3},
			},
			Bindings: map[string]sql.Expression{
				"foo": expression.NewLiteral(int64(2), sql.Int64),
			},
		},
		{
			Query: "SELECT i FROM mytable WHERE s = 'first row' ORDER BY i DESC LIMIT ?;",
			Bindings: map[string]sql.Expression{
				"v1": expression.NewLiteral(1, sql.Int8),
			},
			Expected: []sql.Row{{int64(1)}},
		},
		{
			Query: "SELECT i FROM mytable ORDER BY i LIMIT ? OFFSET 2;",
			Bindings: map[string]sql.Expression{
				"v1": expression.NewLiteral(1, sql.Int8),
				"v2": expression.NewLiteral(1, sql.Int8),
			},
			Expected: []sql.Row{{int64(3)}},
		},
		// todo(max): sort function expressions w/ bindvars are aliased incorrectly
		//{
		//	Query: "SELECT sum(?) as x FROM mytable ORDER BY sum(?)",
		//	Bindings: map[string]sql.Expression{
		//		"v1": expression.NewLiteral(1, sql.Int8),
		//		"v2": expression.NewLiteral(1, sql.Int8),
		//	},
		//	Expected: []sql.Row{{float64(3)}},
		//},
		{
			Query: "SELECT (select sum(?) from mytable) as x FROM mytable ORDER BY (select sum(?) from mytable)",
			Bindings: map[string]sql.Expression{
				"v1": expression.NewLiteral(1, sql.Int8),
				"v2": expression.NewLiteral(1, sql.Int8),
			},
			Expected: []sql.Row{{float64(3)}, {float64(3)}, {float64(3)}},
		},
		{
			Query: "With x as (select sum(?) from mytable) select sum(?) from x ORDER BY (select sum(?) from mytable)",
			Bindings: map[string]sql.Expression{
				"v1": expression.NewLiteral(1, sql.Int8),
				"v2": expression.NewLiteral(1, sql.Int8),
				"v3": expression.NewLiteral(1, sql.Int8),
			},
			Expected: []sql.Row{{float64(1)}},
		},
		{
			Query: "SELECT CAST(? as CHAR) UNION SELECT CAST(? as CHAR)",
			Bindings: map[string]sql.Expression{
				"v1": expression.NewLiteral(1, sql.Int8),
				"v2": expression.NewLiteral("1", sql.TinyText),
			},
			Expected: []sql.Row{{"1"}},
		},
	}

	harness.Setup(setup.MydbData, setup.MytableData)
	e := mustNewEngine(t, harness)
	defer e.Close()

	RunQuery(t, e, harness, "CREATE TABLE a (x int, y int, z int)")
	RunQuery(t, e, harness, "INSERT INTO a VALUES (0,1,1), (1,1,1), (2,1,1), (3,2,2), (4,2,2)")
	for _, tt := range qtests {
		t.Run(fmt.Sprintf("%s", tt.Query), func(t *testing.T) {
			ctx := NewContext(harness)
			_, err := e.PrepareQuery(ctx, tt.Query)
			require.NoError(t, err)
			TestQueryWithContext(t, ctx, e, tt.Query, tt.Expected, tt.ExpectedColumns, tt.Bindings)
		})
	}

	repeatTests := []queries.QueryTest{
		{
			Bindings: map[string]sql.Expression{
				"v1": expression.NewLiteral(int64(2), sql.Int64),
			},
			Expected: []sql.Row{
				{2, float64(4)},
			},
		},
		{
			Bindings: map[string]sql.Expression{
				"v1": expression.NewLiteral(int64(2), sql.Int64),
			},
			Expected: []sql.Row{
				{2, float64(4)},
			},
		},
		{
			Bindings: map[string]sql.Expression{
				"v1": expression.NewLiteral(int64(0), sql.Int64),
			},
			Expected: []sql.Row{
				{1, float64(2)},
				{2, float64(4)},
			},
		},
		{
			Bindings: map[string]sql.Expression{
				"v1": expression.NewLiteral(int64(3), sql.Int64),
			},
			Expected: []sql.Row{
				{2, float64(2)},
			},
		},
		{
			Bindings: map[string]sql.Expression{
				"v1": expression.NewLiteral(int64(1), sql.Int64),
			},
			Expected: []sql.Row{
				{1, float64(1)},
				{2, float64(4)},
			},
		},
	}
	repeatQ := "select y, sum(y) from a where x > ? group by y order by y"
	ctx := NewContext(harness)
	_, err := e.PrepareQuery(ctx, repeatQ)
	require.NoError(t, err)
	for _, tt := range repeatTests {
		t.Run(fmt.Sprintf("%s", tt.Query), func(t *testing.T) {
			TestQueryWithContext(t, ctx, e, repeatQ, tt.Expected, tt.ExpectedColumns, tt.Bindings)
		})
	}
}

var pid uint64

func getEmptyPort(t *testing.T) int {
	listener, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return port
}

func NewContext(harness Harness) *sql.Context {
	return newContextSetup(harness.NewContext())
}

func NewContextWithClient(harness ClientHarness, client sql.Client) *sql.Context {
	return newContextSetup(harness.NewContextWithClient(client))
}

func newContextSetup(ctx *sql.Context) *sql.Context {
	// Select a current database if there isn't one yet
	if ctx.GetCurrentDatabase() == "" {
		ctx.SetCurrentDatabase("mydb")
	}

	// Add our in-session view to the context
	_ = ctx.GetViewRegistry().Register("mydb",
		plan.NewSubqueryAlias(
			"myview",
			"SELECT * FROM mytable",
			plan.NewProject([]sql.Expression{expression.NewStar()}, plan.NewUnresolvedTable("mytable", "mydb")),
		).AsView())

	ctx.ApplyOpts(sql.WithPid(atomic.AddUint64(&pid, 1)))

	// We don't want to show any external procedures in our engine tests, so we exclude them
	_ = ctx.SetSessionVariable(ctx, "show_external_procedures", false)

	return ctx
}

func NewSession(harness Harness) *sql.Context {
	th, ok := harness.(TransactionHarness)
	if !ok {
		panic("Cannot use NewSession except on a TransactionHarness")
	}

	ctx := th.NewSession()
	currentDB := ctx.GetCurrentDatabase()
	if currentDB == "" {
		currentDB = "mydb"
		ctx.WithCurrentDB(currentDB)
	}

	_ = ctx.GetViewRegistry().Register(currentDB,
		plan.NewSubqueryAlias(
			"myview",
			"SELECT * FROM mytable",
			plan.NewProject([]sql.Expression{expression.NewStar()}, plan.NewUnresolvedTable("mytable", "mydb")),
		).AsView())

	ctx.ApplyOpts(sql.WithPid(atomic.AddUint64(&pid, 1)))

	return ctx
}

// NewBaseSession returns a new BaseSession compatible with these tests. Most tests will work with any session
// implementation, but for full compatibility use a session based on this one.
func NewBaseSession() *sql.BaseSession {
	return sql.NewBaseSessionWithClientServer("address", sql.Client{Address: "localhost", User: "root"}, 1)
}

func NewContextWithEngine(harness Harness, engine *sqle.Engine) *sql.Context {
	return NewContext(harness)
}

// NewEngine creates test data and returns an engine using the harness provided.
func NewEngine(t *testing.T, harness Harness) *sqle.Engine {
	dbs := CreateTestData(t, harness)
	engine := NewEngineWithDbs(t, harness, dbs)
	return engine
}

func mustNewEngine(t *testing.T, h Harness) *sqle.Engine {
	e, err := h.NewEngine(t)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// NewSpatialEngine creates test data and returns an engine using the harness provided.
func NewSpatialEngine(t *testing.T, harness Harness) *sqle.Engine {
	dbs := CreateSpatialTestData(t, harness)
	engine := NewEngineWithDbs(t, harness, dbs)
	return engine
}

// NewEngineWithProviderSetup creates test data and returns an engine using the harness provided.
func NewEngineWithProviderSetup(t *testing.T, harness Harness, pro sql.MutableDatabaseProvider, setupData []setup.SetupScript) (*sqle.Engine, error) {
	e := NewEngineWithProvider(t, harness, pro)
	ctx := NewContext(harness)

	var supportsIndexes bool
	if ih, ok := harness.(IndexHarness); ok && ih.SupportsNativeIndexCreation() {
		supportsIndexes = true

	}
	if len(setupData) == 0 {
		setupData = setup.MydbData
	}
	return RunEngineScripts(ctx, e, setupData, supportsIndexes)
}

func RunEngineScripts(ctx *sql.Context, e *sqle.Engine, scripts []setup.SetupScript, supportsIndexes bool) (*sqle.Engine, error) {
	for i := range scripts {
		for _, s := range scripts[i] {
			if !supportsIndexes {
				if strings.Contains("create index", s) {
					continue
				}
			}
			sch, iter, err := e.Query(ctx, s)
			if err != nil {
				return nil, setupErrf(err, s)
			}
			_, err = sql.RowIterToRows(ctx, sch, iter)
			if err != nil {
				return nil, setupErrf(err, s)
			}
		}
	}
	return e, nil
}

func setupErrf(err error, s string) error {
	return fmt.Errorf("failed query '%s': %w", s, err)
}

// NewEngineWithDbs returns a new engine with the databases provided. This is useful if you don't want to implement a
// full harness but want to run your own tests on DBs you create.
func NewEngineWithDbs(t *testing.T, harness Harness, databases []sql.Database) *sqle.Engine {
	databases = append(databases, information_schema.NewInformationSchemaDatabase())
	provider := harness.NewDatabaseProvider(databases...)

	return NewEngineWithProvider(t, harness, provider)
}

// NewEngineWithProvider returns a new engine with the specified provider. This is useful when you don't want to
// implement a full harness, but you need more control over the database provider than the default test MemoryProvider.
func NewEngineWithProvider(_ *testing.T, harness Harness, provider sql.MutableDatabaseProvider) *sqle.Engine {
	var a *analyzer.Analyzer
	if harness.Parallelism() > 1 {
		a = analyzer.NewBuilder(provider).WithParallelism(harness.Parallelism()).Build()
	} else {
		a = analyzer.NewDefault(provider)
	}
	// All tests will run with all privileges on the built-in root account
	a.Catalog.MySQLDb.AddRootAccount()

	engine := sqle.New(a, new(sqle.Config))

	if idh, ok := harness.(IndexDriverHarness); ok {
		idh.InitializeIndexDriver(engine.Analyzer.Catalog.AllDatabases(NewContext(harness)))
	}

	return engine
}

// TestQueryParallel runs a query on the engine given and asserts that results are as expected.
func TestQueryParallel(t *testing.T, harness Harness, q string, expected []sql.Row, expectedCols []*sql.Column) {
	t.Run(q, func(t *testing.T) {
		t.Parallel()
		if sh, ok := harness.(SkippingHarness); ok {
			if sh.SkipQueryTest(q) {
				t.Skipf("Skipping query %s", q)
			}
		}

		e := mustNewEngine(t, harness)
		defer e.Close()
		ctx := NewContext(harness)
		TestQueryWithContext(t, ctx, e, q, expected, expectedCols, nil)
	})
}

// TestQuery runs a query on the engine given and asserts that results are as expected.
func TestQuery(t *testing.T, harness Harness, q string, expected []sql.Row, expectedCols []*sql.Column, bindings map[string]sql.Expression) {
	t.Run(q, func(t *testing.T) {
		if sh, ok := harness.(SkippingHarness); ok {
			if sh.SkipQueryTest(q) {
				t.Skipf("Skipping query %s", q)
			}
		}

		e := mustNewEngine(t, harness)
		defer e.Close()
		ctx := NewContext(harness)
		TestQueryWithContext(t, ctx, e, q, expected, expectedCols, bindings)
	})
}

func TestQueryWithEngine(t *testing.T, harness Harness, e *sqle.Engine, tt queries.QueryTest) {
	t.Run(tt.Query, func(t *testing.T) {
		if sh, ok := harness.(SkippingHarness); ok {
			if sh.SkipQueryTest(tt.Query) {
				t.Skipf("Skipping query %s", tt.Query)
			}
		}

		ctx := NewContext(harness)
		TestQueryWithContext(t, ctx, e, tt.Query, tt.Expected, tt.ExpectedColumns, tt.Bindings)
	})
}

// TestPreparedQuery runs a prepared query on the engine given and asserts that results are as expected.
func TestPreparedQuery(t *testing.T, harness Harness, q string, expected []sql.Row, expectedCols []*sql.Column) {
	t.Run(q, func(t *testing.T) {
		if sh, ok := harness.(SkippingHarness); ok {
			if sh.SkipQueryTest(q) {
				t.Skipf("Skipping query %s", q)
			}
		}
		e := mustNewEngine(t, harness)
		defer e.Close()
		ctx := NewContext(harness)
		TestPreparedQueryWithContext(t, ctx, e, q, expected, expectedCols)
	})
}

func TestPreparedQueryWithEngine(t *testing.T, harness Harness, e *sqle.Engine, tt queries.QueryTest) {
	t.Run(tt.Query, func(t *testing.T) {
		if sh, ok := harness.(SkippingHarness); ok {
			if sh.SkipQueryTest(tt.Query) {
				t.Skipf("Skipping query %s", tt.Query)
			}
		}
		ctx := NewContext(harness)
		TestPreparedQueryWithContext(t, ctx, e, tt.Query, tt.Expected, tt.ExpectedColumns)
	})
}

func runQueryPreparedWithCtx(
	t *testing.T,
	ctx *sql.Context,
	e *sqle.Engine,
	q string,
) ([]sql.Row, sql.Schema, error) {
	require := require.New(t)
	parsed, err := parse.Parse(ctx, q)
	if err != nil {
		return nil, nil, err
	}

	_, isInsert := parsed.(*plan.InsertInto)
	_, isDatabaser := parsed.(sql.Databaser)

	// *ast.MultiAlterDDL parses arbitrary nodes in a *plan.Block
	if bl, ok := parsed.(*plan.Block); ok {
		for _, n := range bl.Children() {
			if _, ok := n.(*plan.InsertInto); ok {
				isInsert = true
			} else if _, ok := n.(sql.Databaser); ok {
				isDatabaser = true
			}

		}
	}
	if isDatabaser && !isInsert {
		// DDL statements don't support prepared statements
		sch, iter, err := e.QueryNodeWithBindings(ctx, q, nil, nil)
		require.NoError(err, "Unexpected error for query %s", q)

		rows, err := sql.RowIterToRows(ctx, sch, iter)
		return rows, sch, err
	}

	bindVars := make(map[string]sql.Expression)
	var bindCnt int
	var foundBindVar bool
	insertBindings := func(expr sql.Expression) (sql.Expression, transform.TreeIdentity, error) {
		switch e := expr.(type) {
		case *expression.Literal:
			varName := fmt.Sprintf("v%d", bindCnt)
			bindVars[varName] = e
			bindCnt++
			return expression.NewBindVar(varName), transform.NewTree, nil
		case *expression.BindVar:
			if _, ok := bindVars[e.Name]; ok {
				return expr, transform.SameTree, nil
			}
			foundBindVar = true
			return expr, transform.NewTree, nil
		default:
			return expr, transform.SameTree, nil
		}
	}
	bound, _, err := transform.NodeWithOpaque(parsed, func(node sql.Node) (sql.Node, transform.TreeIdentity, error) {
		switch n := node.(type) {
		case *plan.InsertInto:
			newSource, _, err := transform.NodeExprs(n.Source, insertBindings)
			if err != nil {
				return nil, transform.SameTree, err
			}
			return n.WithSource(newSource), transform.SameTree, nil
		default:
			return transform.NodeExprs(n, insertBindings)
		}
		return node, transform.SameTree, nil
	})

	if foundBindVar {
		t.Skip()
	}

	prepared, err := e.Analyzer.PrepareQuery(ctx, bound, nil)
	if err != nil {
		return nil, nil, err
	}
	e.CachePreparedStmt(ctx, prepared, q)

	sch, iter, err := e.QueryNodeWithBindings(ctx, q, nil, bindVars)
	require.NoError(err, "Unexpected error for query %s", q)

	rows, err := sql.RowIterToRows(ctx, sch, iter)
	return rows, sch, err
}

func TestPreparedQueryWithContext(
	t *testing.T,
	ctx *sql.Context,
	e *sqle.Engine,
	q string,
	expected []sql.Row,
	expectedCols []*sql.Column,
) {
	require := require.New(t)
	rows, sch, err := runQueryPreparedWithCtx(t, ctx, e, q)
	require.NoError(err, "Unexpected error for query %s", q)

	checkResults(t, require, expected, expectedCols, sch, rows, q)

	require.Equal(0, ctx.Memory.NumCaches())
}

func TestQueryWithContext(t *testing.T, ctx *sql.Context, e *sqle.Engine, q string, expected []sql.Row, expectedCols []*sql.Column, bindings map[string]sql.Expression) {
	ctx = ctx.WithQuery(q)
	require := require.New(t)
	sch, iter, err := e.QueryWithBindings(ctx, q, bindings)
	require.NoError(err, "Unexpected error for query %s", q)

	rows, err := sql.RowIterToRows(ctx, sch, iter)
	require.NoError(err, "Unexpected error for query %s", q)

	checkResults(t, require, expected, expectedCols, sch, rows, q)

	require.Equal(0, ctx.Memory.NumCaches())
}

func checkResults(
	t *testing.T,
	require *require.Assertions,
	expected []sql.Row,
	expectedCols []*sql.Column,
	sch sql.Schema,
	rows []sql.Row,
	q string,
) {
	widenedRows := WidenRows(sch, rows)
	widenedExpected := WidenRows(sch, expected)

	upperQuery := strings.ToUpper(q)
	orderBy := strings.Contains(upperQuery, "ORDER BY ")

	// We replace all times for SHOW statements with the Unix epoch
	if strings.HasPrefix(upperQuery, "SHOW ") {
		for _, widenedRow := range widenedRows {
			for i, val := range widenedRow {
				if _, ok := val.(time.Time); ok {
					widenedRow[i] = time.Unix(0, 0).UTC()
				}
			}
		}
	}

	// .Equal gives better error messages than .ElementsMatch, so use it when possible
	if orderBy || len(expected) <= 1 {
		require.Equal(widenedExpected, widenedRows, "Unexpected result for query %s", q)
	} else {
		require.ElementsMatch(widenedExpected, widenedRows, "Unexpected result for query %s", q)
	}

	// If the expected schema was given, test it as well
	if expectedCols != nil {
		assert.Equal(t, expectedCols, stripSchema(sch))
	}
}

func stripSchema(s sql.Schema) []*sql.Column {
	fields := make([]*sql.Column, len(s))
	for i, c := range s {
		fields[i] = &sql.Column{
			Name: c.Name,
			Type: c.Type,
		}
	}
	return fields
}

func TestJsonScripts(t *testing.T, harness Harness) {
	for _, script := range queries.JsonScripts {
		TestScript(t, harness, script)
	}
}

// For a variety of reasons, the widths of various primitive types can vary when passed through different SQL queries
// (and different database implementations). We may eventually decide that this undefined behavior is a problem, but
// for now it's mostly just an issue when comparing results in tests. To get around this, we widen every type to its
// widest value in actual and expected results.
func WidenRows(sch sql.Schema, rows []sql.Row) []sql.Row {
	widened := make([]sql.Row, len(rows))
	for i, row := range rows {
		widened[i] = WidenRow(sch, row)
	}
	return widened
}

// See WidenRows
func WidenRow(sch sql.Schema, row sql.Row) sql.Row {
	widened := make(sql.Row, len(row))
	for i, v := range row {

		var vw interface{}
		if i < len(sch) && sql.IsJSON(sch[i].Type) {
			widened[i] = widenJSONValues(v)
			continue
		}

		switch x := v.(type) {
		case int:
			vw = int64(x)
		case int8:
			vw = int64(x)
		case int16:
			vw = int64(x)
		case int32:
			vw = int64(x)
		case uint:
			vw = uint64(x)
		case uint8:
			vw = uint64(x)
		case uint16:
			vw = uint64(x)
		case uint32:
			vw = uint64(x)
		case float32:
			vw = float64(x)
		default:
			vw = v
		}
		widened[i] = vw
	}
	return widened
}

func widenJSONValues(val interface{}) sql.JSONValue {
	if val == nil {
		return nil
	}

	js, ok := val.(sql.JSONValue)
	if !ok {
		panic(fmt.Sprintf("%v is not json", val))
	}

	doc, err := js.Unmarshall(sql.NewEmptyContext())
	if err != nil {
		panic(err)
	}

	doc.Val = widenJSON(doc.Val)
	return doc
}

func widenJSON(val interface{}) interface{} {
	switch x := val.(type) {
	case int:
		return float64(x)
	case int8:
		return float64(x)
	case int16:
		return float64(x)
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	case uint:
		return float64(x)
	case uint8:
		return float64(x)
	case uint16:
		return float64(x)
	case uint32:
		return float64(x)
	case uint64:
		return float64(x)
	case float32:
		return float64(x)
	case []interface{}:
		return widenJSONArray(x)
	case map[string]interface{}:
		return widenJSONObject(x)
	default:
		return x
	}
}

func widenJSONObject(narrow map[string]interface{}) (wide map[string]interface{}) {
	wide = make(map[string]interface{}, len(narrow))
	for k, v := range narrow {
		wide[k] = widenJSON(v)
	}
	return
}

func widenJSONArray(narrow []interface{}) (wide []interface{}) {
	wide = make([]interface{}, len(narrow))
	for i, v := range narrow {
		wide[i] = widenJSON(v)
	}
	return
}

func TestPrivilegePersistence(t *testing.T, h Harness) {
	harness, ok := h.(ClientHarness)
	if !ok {
		t.Skip("Cannot run TestPrivilegePersistence as the harness must implement ClientHarness")
	}

	engine := mustNewEngine(t, harness)
	defer engine.Close()
	engine.Analyzer.Catalog.MySQLDb.AddRootAccount()
	ctx := NewContextWithClient(harness, sql.Client{
		User:    "root",
		Address: "localhost",
	})

	var users []*mysql_db.User
	var roles []*mysql_db.RoleEdge
	engine.Analyzer.Catalog.MySQLDb.SetPersistCallback(
		func(ctx *sql.Context, buf []byte) error {
			// erase everything from users and roles
			users = make([]*mysql_db.User, 0)
			roles = make([]*mysql_db.RoleEdge, 0)

			// Deserialize the flatbuffer
			serialMySQLDb := serial.GetRootAsMySQLDb(buf, 0)

			// Fill in users
			for i := 0; i < serialMySQLDb.UserLength(); i++ {
				serialUser := new(serial.User)
				if !serialMySQLDb.User(serialUser, i) {
					continue
				}
				user := mysql_db.LoadUser(serialUser)
				users = append(users, user)
			}

			// Fill in roles
			for i := 0; i < serialMySQLDb.RoleEdgesLength(); i++ {
				serialRoleEdge := new(serial.RoleEdge)
				if !serialMySQLDb.RoleEdges(serialRoleEdge, i) {
					continue
				}
				role := mysql_db.LoadRoleEdge(serialRoleEdge)
				roles = append(roles, role)
			}
			return nil
		},
	)

	RunQueryWithContext(t, engine, ctx, "CREATE USER tester@localhost")
	// If the user exists in []*mysql_db.User, then it must be NOT nil.
	require.NotNil(t, findUser("tester", "localhost", users))

	RunQueryWithContext(t, engine, ctx, "INSERT INTO mysql.user (Host, User) VALUES ('localhost', 'tester1')")
	require.Nil(t, findUser("tester1", "localhost", users))

	RunQueryWithContext(t, engine, ctx, "UPDATE mysql.user SET User = 'test_user' WHERE User = 'tester'")
	require.NotNil(t, findUser("tester", "localhost", users))

	RunQueryWithContext(t, engine, ctx, "FLUSH PRIVILEGES")
	require.NotNil(t, findUser("tester1", "localhost", users))
	require.Nil(t, findUser("tester", "localhost", users))
	require.NotNil(t, findUser("test_user", "localhost", users))

	RunQueryWithContext(t, engine, ctx, "DELETE FROM mysql.user WHERE User = 'tester1'")
	require.NotNil(t, findUser("tester1", "localhost", users))

	RunQueryWithContext(t, engine, ctx, "GRANT SELECT ON mydb.* TO test_user@localhost")
	user := findUser("test_user", "localhost", users)
	require.True(t, user.PrivilegeSet.Database("mydb").Has(sql.PrivilegeType_Select))

	RunQueryWithContext(t, engine, ctx, "UPDATE mysql.db SET Insert_priv = 'Y' WHERE User = 'test_user'")
	require.False(t, user.PrivilegeSet.Database("mydb").Has(sql.PrivilegeType_Insert))

	RunQueryWithContext(t, engine, ctx, "CREATE USER dolt@localhost")
	RunQueryWithContext(t, engine, ctx, "INSERT INTO mysql.db (Host, Db, User, Select_priv) VALUES ('localhost', 'mydb', 'dolt', 'Y')")
	user1 := findUser("dolt", "localhost", users)
	require.NotNil(t, user1)
	require.False(t, user1.PrivilegeSet.Database("mydb").Has(sql.PrivilegeType_Select))

	RunQueryWithContext(t, engine, ctx, "FLUSH PRIVILEGES")
	require.Nil(t, findUser("tester1", "localhost", users))
	user = findUser("test_user", "localhost", users)
	require.True(t, user.PrivilegeSet.Database("mydb").Has(sql.PrivilegeType_Insert))
	user1 = findUser("dolt", "localhost", users)
	require.True(t, user1.PrivilegeSet.Database("mydb").Has(sql.PrivilegeType_Select))

	RunQueryWithContext(t, engine, ctx, "CREATE ROLE test_role")
	RunQueryWithContext(t, engine, ctx, "GRANT SELECT ON *.* TO test_role")
	require.Zero(t, len(roles))
	RunQueryWithContext(t, engine, ctx, "GRANT test_role TO test_user@localhost")
	require.NotZero(t, len(roles))

	RunQueryWithContext(t, engine, ctx, "UPDATE mysql.role_edges SET to_user = 'tester2' WHERE to_user = 'test_user'")
	require.NotNil(t, findRole("test_user", roles))
	require.Nil(t, findRole("tester2", roles))

	RunQueryWithContext(t, engine, ctx, "FLUSH PRIVILEGES")
	require.Nil(t, findRole("test_user", roles))
	require.NotNil(t, findRole("tester2", roles))

	RunQueryWithContext(t, engine, ctx, "INSERT INTO mysql.role_edges VALUES ('%', 'test_role', 'localhost', 'test_user', 'N')")
	require.Nil(t, findRole("test_user", roles))

	RunQueryWithContext(t, engine, ctx, "FLUSH PRIVILEGES")
	require.NotNil(t, findRole("test_user", roles))

	_, _, err := engine.Query(ctx, "FLUSH NO_WRITE_TO_BINLOG PRIVILEGES")
	require.Error(t, err)

	_, _, err = engine.Query(ctx, "FLUSH LOCAL PRIVILEGES")
	require.Error(t, err)
}

// findUser returns *mysql_db.User corresponding to specific user and host names.
// If not found, returns nil *mysql_db.User.
func findUser(user string, host string, users []*mysql_db.User) *mysql_db.User {
	for _, u := range users {
		if u.User == user && u.Host == host {
			return u
		}
	}
	return nil
}

// findRole returns *mysql_db.RoleEdge corresponding to specific to_user.
// If not found, returns nil *mysql_db.RoleEdge.
func findRole(toUser string, roles []*mysql_db.RoleEdge) *mysql_db.RoleEdge {
	for _, r := range roles {
		if r.ToUser == toUser {
			return r
		}
	}
	return nil
}
