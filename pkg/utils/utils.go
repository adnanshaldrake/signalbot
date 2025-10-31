package utils

import (
	"adnan/binance-bot/pkg/config"
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// HTTPClient interface
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func ChunkSlice(slice []string, chunkSize int) ([][]string, error) {
	/*
		ChunkSlice divides the input slice into chunks of the given chunk size
		returns a 2D slice of strings, where each element of the slice
		a chunk of the input slice. If the input slice is nil or the chunk
		size is less than 1, an error is returned.

		Parameters:
		- slice: the input slice to be chunked
		- chunkSize: the size of each chunk

		Returns:
		- [][]string: the chunked slices
		- error: an error if the input slice is nil or the chunk size is invalid

		Example usage:
			slice := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
			chunks, err := ChunkSlice(slice, 3)
			if err != nil {
			log.Fatal(err)
			}
			fmt.Println(chunks) // Output: [[a b c] [d e f] [g h i]]
	*/
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
			end = len((slice))
		}
		chunks = append(chunks, slice[i:end])
	}
	return chunks, nil
}

func GetData(client HTTPClient, endpointURL string) ([]byte, error) {
	/*
		GetData sends an HTTP GET request to the specified URL
		and returns the response body and status code.
		An error is returned if the request fails or if the
		response cannot be read.

		url: The URL to send the GET request to.

		Returns:
		    - int: The HTTP status code of the response.
		    - []byte: The response body as a byte slice.
		    - error: If an error occurred while sending
					 the request or reading the response.
	*/
	url := config.DataUrl.String() + endpointURL
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create GET request: %s\n", err)
	}
	res, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("http GET request failed: %s\n", err)
	}
	defer res.Body.Close()

	resBody, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %s\n", err)
	}
	return resBody, nil
}

func GetContextWithTimeout(interval int) (context.Context, context.CancelFunc) {
	// Used to carry the deadline value for the Redis
	ctx := context.Background()

	// Use the context to manage a timeout for the Redis operation
	ctx, cancel := context.WithTimeout(ctx, time.Duration(interval)*time.Second)
	return ctx, cancel
}

// TODO Add TLS config
func GetRedisClient() (*redis.Client, error) {
	/*
		Creates a new Redis client with preferred settings and
		returns a context, Redis client instance, and an error.

		Parameters: None.

		Return Values
		context.Context: A context.Context instance that can be
				used to manage a timeout for the Redis operation.
		*redis.Client: A pointer to a Redis client instance.
		error: An error value that is non-nil if there was
			   an issue with creating the Redis client.
	*/
	r_url := os.Getenv("REDIS_URL")
	if r_url == "" {
		r_url = "localhost:6379"
	}
	r_creds := os.Getenv("REDIS_CREDS")
	if r_creds == "" {
		r_creds = ":"
	}
	creds := strings.Split(r_creds, ":")
	// Create a new Redis client with preferred settings
	rdb := redis.NewClient(&redis.Options{
		Addr:     r_url,
		Username: creds[0],
		Password: creds[1],
		DB:       0, // use default DB
	})

	ctx, cancel := GetContextWithTimeout(10)

	// Test with a ping before returning client
	if err := rdb.Ping(ctx).Err(); err != nil {
		cancel()
		return nil, err
	}

	return rdb, nil
}
