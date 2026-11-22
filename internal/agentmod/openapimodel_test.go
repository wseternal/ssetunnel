package agentmod

import (
	"testing"

	"github.com/wseternal/ssetunnel/internal/moduleproto"
)

const operationModelsFixture = `
openapi: 3.0.3
info:
  title: Fixture API
  version: 1.0.0
paths:
  /jobs/{job_id}:
    parameters:
      - name: job_id
        in: path
        required: true
        schema: {type: string}
      - name: verbose
        in: query
        description: include detail
        schema: {type: boolean}
    get:
      operationId: getJob
      summary: Fetch one job
      description: Returns the job record.
      parameters:
        - name: verbose
          in: query
          schema: {type: boolean, default: false}
    delete:
      operationId: deleteJob
  /jobs:
    post:
      operationId: submitJob
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/SubmitRequest'
  /health:
    get:
      operationId: health
components:
  schemas:
    SubmitRequest:
      type: object
      required: [prompt, attachments]
      properties:
        prompt:
          type: string
          description: the instruction
          example: hello
        mode:
          type: string
          enum: [fast, slow]
          default: fast
        attachments:
          type: array
          items:
            $ref: '#/components/schemas/FilePayload'
        tree:
          $ref: '#/components/schemas/Node'
    FilePayload:
      allOf:
        - $ref: '#/components/schemas/BaseFile'
        - type: object
          properties:
            content:
              type: string
    BaseFile:
      type: object
      required: [name]
      properties:
        name:
          type: string
        size:
          type: integer
          format: int64
    Node:
      type: object
      properties:
        label:
          type: string
        child:
          $ref: '#/components/schemas/Node'
`

func TestBuildOperationModels(t *testing.T) {
	title, ops, err := BuildOperationModels([]byte(operationModelsFixture))
	if err != nil {
		t.Fatalf("BuildOperationModels: %v", err)
	}
	if title != "Fixture API" {
		t.Errorf("title = %q, want Fixture API", title)
	}
	if len(ops) != 4 {
		t.Fatalf("len(ops) = %d, want 4: %+v", len(ops), ops)
	}
	// Paths sort lexicographically, then canonical method order.
	wantOrder := []string{"GET /health", "POST /jobs", "GET /jobs/{job_id}", "DELETE /jobs/{job_id}"}
	for i, want := range wantOrder {
		got := ops[i].Method + " " + ops[i].Path
		if got != want {
			t.Errorf("ops[%d] = %q, want %q", i, got, want)
		}
	}
}

func TestBuildOperationModelsPathParameters(t *testing.T) {
	_, ops, err := BuildOperationModels([]byte(operationModelsFixture))
	if err != nil {
		t.Fatalf("BuildOperationModels: %v", err)
	}
	getJob := ops[2]
	if getJob.OperationID != "getJob" {
		t.Fatalf("operation id = %q", getJob.OperationID)
	}
	if len(getJob.Parameters) != 2 {
		t.Fatalf("parameters = %+v, want path + overridden query", getJob.Parameters)
	}
	if !getJob.Parameters[0].Required {
		t.Error("path parameter must be required even when the spec omits it")
	}
	// The operation-level entry replaces the path-level one in place.
	verbose := getJob.Parameters[1]
	if verbose.In != "query" || verbose.Schema == nil || verbose.Schema.Default != false {
		t.Errorf("verbose = %+v, want overridden schema with default false", verbose)
	}
	if getJob.RequestBody != nil {
		t.Error("GET should not report a request body")
	}
}

func TestBuildOperationModelsBodyRefResolution(t *testing.T) {
	_, ops, err := BuildOperationModels([]byte(operationModelsFixture))
	if err != nil {
		t.Fatalf("BuildOperationModels: %v", err)
	}
	submit := ops[1]
	if submit.RequestBody == nil {
		t.Fatal("POST /jobs missing request body")
	}
	if !submit.RequestBody.Required {
		t.Error("request body should be required")
	}
	if submit.RequestBody.ContentType != "application/json" {
		t.Errorf("content type = %q", submit.RequestBody.ContentType)
	}
	body := submit.RequestBody.Schema
	if body == nil || body.Ref != "SubmitRequest" || body.Type != "object" {
		t.Fatalf("body schema = %+v", body)
	}
	if len(body.Properties) != 4 {
		t.Fatalf("properties = %+v, want 4", body.Properties)
	}
	// Properties are sorted by name; required flags come from the schema.
	names := []string{"attachments", "mode", "prompt", "tree"}
	for i, name := range names {
		if body.Properties[i].Name != name {
			t.Errorf("properties[%d] = %q, want %q", i, body.Properties[i].Name, name)
		}
	}
	prompt := body.Properties[2]
	if !prompt.Required || prompt.Schema.Example != "hello" {
		t.Errorf("prompt = %+v, want required with example", prompt)
	}
	mode := body.Properties[1]
	if len(mode.Schema.Enum) != 2 || mode.Schema.Default != "fast" {
		t.Errorf("mode = %+v, want enum + default", mode.Schema)
	}
}

func TestBuildOperationModelsAllOfMerge(t *testing.T) {
	_, ops, err := BuildOperationModels([]byte(operationModelsFixture))
	if err != nil {
		t.Fatalf("BuildOperationModels: %v", err)
	}
	body := ops[1].RequestBody.Schema
	var attachments *APISchema
	for _, prop := range body.Properties {
		if prop.Name == "attachments" {
			attachments = prop.Schema
		}
	}
	if attachments == nil || attachments.Type != "array" || attachments.Items == nil {
		t.Fatalf("attachments = %+v", attachments)
	}
	item := attachments.Items
	if item.Ref != "FilePayload" {
		t.Errorf("item ref = %q", item.Ref)
	}
	if len(item.Properties) != 3 {
		t.Fatalf("allOf merge properties = %+v, want name/size/content", item.Properties)
	}
	for _, prop := range item.Properties {
		if prop.Name == "name" && !prop.Required {
			t.Error("required list from the allOf member was lost")
		}
		if prop.Name == "size" && prop.Schema.Format != "int64" {
			t.Errorf("size format = %q", prop.Schema.Format)
		}
	}
}

func TestBuildOperationModelsRecursion(t *testing.T) {
	_, ops, err := BuildOperationModels([]byte(operationModelsFixture))
	if err != nil {
		t.Fatalf("BuildOperationModels: %v", err)
	}
	body := ops[1].RequestBody.Schema
	var tree *APISchema
	for _, prop := range body.Properties {
		if prop.Name == "tree" {
			tree = prop.Schema
		}
	}
	if tree == nil || tree.Ref != "Node" {
		t.Fatalf("tree = %+v", tree)
	}
	var child *APISchema
	for _, prop := range tree.Properties {
		if prop.Name == "child" {
			child = prop.Schema
		}
	}
	if child == nil || !child.Recursive || child.Ref != "Node" {
		t.Errorf("child = %+v, want recursive Node marker", child)
	}
}

func TestBuildOperationModelsRejectsOversize(t *testing.T) {
	big := make([]byte, moduleproto.MaxOpenAPISize+1)
	if _, _, err := BuildOperationModels(big); err == nil {
		t.Fatal("expected size error")
	}
}

// OpenAPI 3.1 allows array types and referenced path items; both must survive
// the flattening step or the console would hide operations or mis-type fields.
const operationModelsFixture31 = `
openapi: 3.1.0
info:
  title: V31 API
  version: 1.0.0
components:
  pathItems:
    Ping:
      get:
        operationId: ping
        responses: {}
paths:
  /ping:
    $ref: '#/components/pathItems/Ping'
  /note:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                text:
                  type: [string, null]
`

func TestBuildOperationModelsOpenAPI31(t *testing.T) {
	_, ops, err := BuildOperationModels([]byte(operationModelsFixture31))
	if err != nil {
		t.Fatalf("BuildOperationModels: %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("len(ops) = %d, want 2 (path item $ref must be followed): %+v", len(ops), ops)
	}
	var note *APIOperation
	for i := range ops {
		if ops[i].Path == "/note" {
			note = &ops[i]
		}
	}
	if note == nil || note.Method != "POST" {
		t.Fatalf("missing POST /note: %+v", ops)
	}
	text := note.RequestBody.Schema.Properties[0]
	if text.Schema.Type != "string" || !text.Schema.Nullable {
		t.Errorf("text schema = %+v, want string + nullable", text.Schema)
	}
}
