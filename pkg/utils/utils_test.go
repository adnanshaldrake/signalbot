package utils

import (
	"bytes"
	"io"
	"net/http"
	"reflect"
	"testing"
)

type MockHTTPClient struct {
	Response *http.Response
	Err      error
}

func (c *MockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return c.Response, c.Err
}

func TestChunkSlice(t *testing.T) {
	// Test case 1: slice is nil
	slice := []string(nil)
	chunks, err := ChunkSlice(slice, 3)
	if chunks != nil || err == nil {
		t.Errorf("ChunkSlice(%v, %d) = (%v, %v); expected (nil, error)", slice, 3, chunks, err)
	}

	// Test case 2: chunk size is less than 1
	slice = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
	chunks, err = ChunkSlice(slice, 0)
	if chunks != nil || err == nil {
		t.Errorf("ChunkSlice(%v, %d) = (%v, %v); expected (nil, error)", slice, 0, chunks, err)
	}

	// Test case 3: slice is not empty and chunk size is valid
	slice = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
	expectedChunks := [][]string{{"a", "b", "c"}, {"d", "e", "f"}, {"g", "h", "i"}}
	chunks, err = ChunkSlice(slice, 3)
	if err != nil {
		t.Errorf("ChunkSlice(%v, %d) returned error: %v", slice, 3, err)
	}
	if !reflect.DeepEqual(chunks, expectedChunks) {
		t.Errorf("ChunkSlice(%v, %d) = %v; expected %v", slice, 3, chunks, expectedChunks)
	}
}

func TestGetData(t *testing.T) {
	// Create a mock HTTP client that returns a test response
	client := &MockHTTPClient{
		Response: &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString("test response body")),
		},
		Err: nil,
	}

	// Call the function being tested with the mock client
	body, err := GetData(client, "/test")

	// Check that the function returned the expected result
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if string(body) != "test response body" {
		t.Errorf("GetData returned an unexpected response body: got %q, want %q", body, "test response body")
	}
}
