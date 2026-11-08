package utils

import (
	"fmt"
	"io"
	"net/http"

	"github.com/adnanshaldrake/signalbot/pkg/config"
)

// HTTPClient interface
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// ChunkSlice divides the input slice into chunks of the given chunk size.
func ChunkSlice(slice []string, chunkSize int) ([][]string, error) {
	if slice == nil {
		return nil, fmt.Errorf("input slice is nil")
	}
	if chunkSize < 1 {
		return nil, fmt.Errorf("invalid chunk size")
	}

	var chunks [][]string
	for i := 0; i < len(slice); i += chunkSize {
		end := i + chunkSize
		if end > len(slice) {
			end = len(slice)
		}
		chunks = append(chunks, slice[i:end])
	}
	return chunks, nil
}

// GetData sends an HTTP GET request to the specified endpoint URL.
func GetData(client HTTPClient, endpointURL string) ([]byte, error) {
	url := config.DataUrl.String() + endpointURL
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create GET request: %w", err)
	}
	res, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("http GET request failed: %w", err)
	}
	defer res.Body.Close()

	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	return resBody, nil
}
