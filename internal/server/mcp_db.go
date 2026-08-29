package server

// Central-MCP database tools — thin faces over the app-database core
// (appdb.go), registered on /mcp beside the app and service tools. They take
// the target string directly — "global" or "app:<nonce>" — so one tool serves
// both, and gateDBTarget decides who may touch what.
//
// db_query is read-only, permanently. db_execute is the same core function
// with the safety off, available only when mcp_database_mode is "full-access".
// There is no db_delete_database — dropping a database is irreversible and
// takes one confident sentence to talk a model into, so it lives in the
// control panel with a human and a confirmation step.

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpDBMode returns the database-access mode for central MCP tools.
// "read-only" is the default; "full-access" must be set on purpose.
func (s *Server) mcpDBMode() string {
	if v, err := s.store.GetSetting("mcp_database_mode"); err == nil && v == "full-access" {
		return "full-access"
	}
	return "read-only"
}

// registerDatabaseTools wires the four central database tools onto the MCP
// server. All roles see them; gateDBTarget is the permission, not the mount.
func (s *Server) registerDatabaseTools(mcps *mcp.Server) {
	dbTargetProp := map[string]interface{}{
		"type":        "string",
		"description": "Database target: \"global\" for a shared database, or \"app:<nonce>\" for an app's own.",
	}
	dbNameProp := map[string]interface{}{
		"type":        "string",
		"description": "Database name (defaults to \"app\").",
	}

	// db_list_databases
	mcps.AddTool(&mcp.Tool{
		Name:        "db_list_databases",
		Description: "List databases for a target (name, size, mtime).",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"target": dbTargetProp,
			},
			"required": []string{"target"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		target, _ := args["target"].(string)
		dbs, err := s.coreDBList(user, target)
		if err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(map[string]interface{}{"databases": dbs})
	})

	// db_schema
	mcps.AddTool(&mcp.Tool{
		Name:        "db_schema",
		Description: "Get a database's schema: tables, columns, indexes, and DDL. The one tool that matters for writing queries blind.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"target":   dbTargetProp,
				"database": dbNameProp,
			},
			"required": []string{"target"},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		target, _ := args["target"].(string)
		name, _ := args["database"].(string)
		schema, err := s.coreDBSchema(user, target, name)
		if err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(schema)
	})

	// db_query — read-only, permanently. A write sent here gets the same
	// friendly message as a write blocked by the mode setting, rather than
	// SQLite's "attempt to write a readonly database".
	mcps.AddTool(&mcp.Tool{
		Name:        "db_query",
		Description: "Run a single SQL statement (read-only, always) or a batch-in-one-transaction against a database. Parameters bind by position (array) or by name (object).",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"target":   dbTargetProp,
				"database": dbNameProp,
				"sql":      map[string]interface{}{"type": "string", "description": "A single SQL statement."},
				"params":   map[string]interface{}{"description": "Positional array or named object of bind parameters."},
				"batch":    map[string]interface{}{"type": "array", "description": "Statements run in one all-or-nothing transaction."},
			},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		res, err := s.coreDBQuery(user, args["target"].(string), dbReqFromArgs(args), true)
		if err != nil {
			if isReadonlyWriteErr(err) {
				return mcpToolError("%v", errMCPDatabaseReadOnly), nil
			}
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(res)
	})

	// db_execute — writes allowed only when mcp_database_mode is "full-access".
	// Not a privilege boundary (an admin could do the same via HTTP); accident
	// prevention for a model asked to "clean up the old rows."
	mcps.AddTool(&mcp.Tool{
		Name:        "db_execute",
		Description: "Run a statement or batch that may write, against a database. Requires MCP database mode = full-access.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"target":   dbTargetProp,
				"database": dbNameProp,
				"sql":      map[string]interface{}{"type": "string", "description": "A single SQL statement."},
				"params":   map[string]interface{}{"description": "Positional array or named object of bind parameters."},
				"batch":    map[string]interface{}{"type": "array", "description": "Statements run in one all-or-nothing transaction."},
			},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		user, err := s.mcpUser(req)
		if err != nil {
			return mcpToolError("auth: %v", err), nil
		}
		if s.mcpDBMode() != "full-access" {
			return mcpToolError("%v", errMCPDatabaseReadOnly), nil
		}
		args := make(map[string]interface{})
		json.Unmarshal(req.Params.Arguments, &args)
		res, err := s.coreDBQuery(user, args["target"].(string), dbReqFromArgs(args), false)
		if err != nil {
			return mcpToolError("%v", err), nil
		}
		return mcpToolResult(res)
	})
}

// dbReqFromArgs builds a dbQueryRequest from parsed MCP tool arguments.
func dbReqFromArgs(args map[string]interface{}) dbQueryRequest {
	var req dbQueryRequest
	req.DB, _ = args["database"].(string)
	req.SQL, _ = args["sql"].(string)
	req.Params = args["params"]
	if batch, ok := args["batch"].([]interface{}); ok {
		for _, b := range batch {
			m, ok := b.(map[string]interface{})
			if !ok {
				continue
			}
			stmt := dbStmtRequest{}
			stmt.SQL, _ = m["sql"].(string)
			stmt.Params = m["params"]
			req.Batch = append(req.Batch, stmt)
		}
	}
	return req
}
