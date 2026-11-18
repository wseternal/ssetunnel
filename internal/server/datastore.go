package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wseternal/ssetunnel/internal/moduleproto"
	"github.com/wseternal/ssetunnel/internal/mux"
	"github.com/wseternal/ssetunnel/sdk"
)

// DataStore handles module data store requests against PostgreSQL.
// It processes CRUD operations on the module_records table, forwarded
// from modules through the agent's control tunnel.
type DataStore struct {
	pool *pgxpool.Pool
}

// NewDataStore creates a data store backed by the given connection pool.
// Returns nil if pool is nil (data store disabled).
func NewDataStore(pool *pgxpool.Pool) *DataStore {
	if pool == nil {
		return nil
	}
	return &DataStore{pool: pool}
}

// HandleRequest processes a data store control message and returns
// the response payload. The caller is responsible for sending the
// response back on the control stream.
func (ds *DataStore) HandleRequest(msg *mux.ControlMessage) *mux.ControlMessage {
	var req moduleproto.DataStoreRequestPayload
	if err := json.Unmarshal(msg.Payload, &req); err != nil {
		return ds.errorResponse(req.RequestID, "invalid request payload")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp := ds.execute(ctx, req)
	payload, err := json.Marshal(resp)
	if err != nil {
		return ds.errorResponse(req.RequestID, "marshal response failed")
	}
	return &mux.ControlMessage{
		Type:     mux.MsgDataStoreResp,
		StreamID: 0,
		Payload:  payload,
	}
}

func (ds *DataStore) execute(ctx context.Context, req moduleproto.DataStoreRequestPayload) moduleproto.DataStoreResponsePayload {
	// Validate module_id format to prevent injection.
	if !sdk.ModuleIDRegexp.MatchString(req.ModuleID) {
		return moduleproto.DataStoreResponsePayload{
			RequestID: req.RequestID,
			Error:     "invalid module_id format",
		}
	}
	switch req.Op {
	case moduleproto.DataStoreCreate:
		return ds.create(ctx, req)
	case moduleproto.DataStoreGet:
		return ds.get(ctx, req)
	case moduleproto.DataStoreList:
		return ds.list(ctx, req)
	case moduleproto.DataStoreDelete:
		return ds.delete(ctx, req)
	default:
		return moduleproto.DataStoreResponsePayload{
			RequestID: req.RequestID,
			Error:     fmt.Sprintf("unknown operation: %s", req.Op),
		}
	}
}

func (ds *DataStore) create(ctx context.Context, req moduleproto.DataStoreRequestPayload) moduleproto.DataStoreResponsePayload {
	if req.ModuleID == "" || req.RecordID == "" {
		return moduleproto.DataStoreResponsePayload{
			RequestID: req.RequestID,
			Error:     "module_id and record_id are required for create",
		}
	}
	data := req.Data
	if data == nil {
		data = json.RawMessage("{}")
	}

	now := time.Now().UTC()
	_, err := ds.pool.Exec(ctx,
		`INSERT INTO module_records (module_id, record_id, data, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (module_id, record_id)
		 DO UPDATE SET data = $3, updated_at = $5`,
		req.ModuleID, req.RecordID, data, now, now)
	if err != nil {
		log.Printf("datastore: create error: %v", err)
		return moduleproto.DataStoreResponsePayload{
			RequestID: req.RequestID,
			Error:     "database error",
		}
	}
	return moduleproto.DataStoreResponsePayload{
		RequestID: req.RequestID,
		Record: &moduleproto.DataStoreRecord{
			ModuleID:  req.ModuleID,
			RecordID:  req.RecordID,
			Data:      data,
			CreatedAt: now.Format(time.RFC3339Nano),
			UpdatedAt: now.Format(time.RFC3339Nano),
		},
	}
}

func (ds *DataStore) get(ctx context.Context, req moduleproto.DataStoreRequestPayload) moduleproto.DataStoreResponsePayload {
	if req.ModuleID == "" || req.RecordID == "" {
		return moduleproto.DataStoreResponsePayload{
			RequestID: req.RequestID,
			Error:     "module_id and record_id are required for get",
		}
	}

	var rec moduleproto.DataStoreRecord
	var createdAt, updatedAt time.Time
	err := ds.pool.QueryRow(ctx,
		`SELECT module_id, record_id, data, created_at, updated_at
		 FROM module_records WHERE module_id = $1 AND record_id = $2`,
		req.ModuleID, req.RecordID).Scan(
		&rec.ModuleID, &rec.RecordID, &rec.Data, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return moduleproto.DataStoreResponsePayload{
				RequestID: req.RequestID,
				Error:     "record not found",
			}
		}
		log.Printf("datastore: get(%s, %s) error: %v", req.ModuleID, req.RecordID, err)
		return moduleproto.DataStoreResponsePayload{
			RequestID: req.RequestID,
			Error:     "database error",
		}
	}
	rec.CreatedAt = createdAt.Format(time.RFC3339Nano)
	rec.UpdatedAt = updatedAt.Format(time.RFC3339Nano)
	return moduleproto.DataStoreResponsePayload{
		RequestID: req.RequestID,
		Record:    &rec,
	}
}

func (ds *DataStore) list(ctx context.Context, req moduleproto.DataStoreRequestPayload) moduleproto.DataStoreResponsePayload {
	if req.ModuleID == "" {
		return moduleproto.DataStoreResponsePayload{
			RequestID: req.RequestID,
			Error:     "module_id is required for list",
		}
	}

	limit := 100
	offset := 0
	if req.Filter != nil {
		if req.Filter.Limit > 0 {
			limit = req.Filter.Limit
		}
		if req.Filter.Offset > 0 {
			offset = req.Filter.Offset
		}
	}
	if limit > 1000 {
		limit = 1000
	}

	rows, err := ds.pool.Query(ctx,
		`SELECT module_id, record_id, data, created_at, updated_at
		 FROM module_records WHERE module_id = $1
		 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		req.ModuleID, limit, offset)
	if err != nil {
		log.Printf("datastore: list error: %v", err)
		return moduleproto.DataStoreResponsePayload{
			RequestID: req.RequestID,
			Error:     "database error",
		}
	}
	defer rows.Close()

	records := make([]moduleproto.DataStoreRecord, 0) // initialized to avoid JSON null
	for rows.Next() {
		var rec moduleproto.DataStoreRecord
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&rec.ModuleID, &rec.RecordID, &rec.Data, &createdAt, &updatedAt); err != nil {
			log.Printf("datastore: scan error: %v", err)
			continue
		}
		rec.CreatedAt = createdAt.Format(time.RFC3339Nano)
		rec.UpdatedAt = updatedAt.Format(time.RFC3339Nano)
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		log.Printf("datastore: list(%s) iteration error: %v", req.ModuleID, err)
		return moduleproto.DataStoreResponsePayload{
			RequestID: req.RequestID,
			Error:     "database iteration error",
		}
	}

	return moduleproto.DataStoreResponsePayload{
		RequestID: req.RequestID,
		Records:   records,
	}
}

func (ds *DataStore) delete(ctx context.Context, req moduleproto.DataStoreRequestPayload) moduleproto.DataStoreResponsePayload {
	if req.ModuleID == "" || req.RecordID == "" {
		return moduleproto.DataStoreResponsePayload{
			RequestID: req.RequestID,
			Error:     "module_id and record_id are required for delete",
		}
	}

	tag, err := ds.pool.Exec(ctx,
		`DELETE FROM module_records WHERE module_id = $1 AND record_id = $2`,
		req.ModuleID, req.RecordID)
	if err != nil {
		log.Printf("datastore: delete error: %v", err)
		return moduleproto.DataStoreResponsePayload{
			RequestID: req.RequestID,
			Error:     "database error",
		}
	}
	return moduleproto.DataStoreResponsePayload{
		RequestID: req.RequestID,
		Deleted:   tag.RowsAffected(),
	}
}

func (ds *DataStore) errorResponse(requestID, message string) *mux.ControlMessage {
	resp := moduleproto.DataStoreResponsePayload{
		RequestID: requestID,
		Error:     message,
	}
	payload, _ := json.Marshal(resp)
	return &mux.ControlMessage{
		Type:     mux.MsgDataStoreResp,
		StreamID: 0,
		Payload:  payload,
	}
}

