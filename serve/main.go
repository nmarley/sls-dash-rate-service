package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/go-redis/redis"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
)

// Response is of type APIGatewayProxyResponse since we're leveraging the
// AWS Lambda Proxy Request functionality (default behavior)
//
// https://serverless.com/framework/docs/providers/aws/events/apigateway/#lambda-proxy-integration
type Response events.APIGatewayProxyResponse

// Handler is our lambda handler invoked by the `lambda.Start` function call
func Handler(ctx context.Context) (Response, error) {
	rates, err := getDashUSDRates()
	if err != nil {
		return Response{StatusCode: 404}, err
	}

	body, err := json.Marshal(rates)
	if err != nil {
		return Response{StatusCode: 404}, err
	}
	resp := Response{
		StatusCode:      200,
		IsBase64Encoded: false,
		Body:            string(body),
		Headers: map[string]string{
			"Content-Type":           "application/json",
			"X-MyCompany-Func-Reply": "serve-handler",

			// Set CORS headers
			"Access-Control-Allow-Headers": "X-Requested-With,Content-Type",
			"Access-Control-Allow-Origin":  "*",
			"Access-Control-Allow-Methods": "GET, HEAD, OPTIONS",
		},
	}

	return resp, nil
}

func main() {
	lambda.Start(Handler)
}

// getDashUSDRates gets exchange rates from Redis
func getDashUSDRates() ([]DashUSDRate, error) {
	var emptyRates []DashUSDRate

	// ensure required environment variables set
	if err := envCheck([]string{"REDIS_URL"}); err != nil {
		return emptyRates, err
	}

	// establish redis connection
	redisCli, err := redisCliCheck(os.Getenv("REDIS_URL"))
	if err != nil {
		return emptyRates, err
	}

	// Get keys to loop thru
	exchanges, err := redisCli.Keys("*").Result()
	if err != nil {
		return emptyRates, err
	}

	// Get all rates from Redis
	var ratesUSD []DashUSDRate
	for _, exch := range exchanges {
		res, err := redisCli.Get(exch).Result()
		if err != nil {
			return emptyRates, err
		}
		var rate DashUSDRate
		if err := rate.UnmarshalBinary([]byte(res)); err != nil {
			return emptyRates, err
		}
		ratesUSD = append(ratesUSD, rate)
	}
	return ratesUSD, nil
}

// envCheck is called upon startup to ensure the required environment variables
// are set
func envCheck(reqd []string) error {
	// ensure config vars set
	missing := false
	for _, env := range reqd {
		val, ok := os.LookupEnv(env)
		if !ok || (len(val) == 0) {
			missing = true
		}
	}
	if missing {
		return fmt.Errorf("at least some required env var not set")
	}
	return nil
}

// redisCliCheck creates a Redis client and checks the connection via PING.
func redisCliCheck(redisURL string) (*redis.Client, error) {
	// establish redis connection
	redisCli := redis.NewClient(&redis.Options{
		Addr:     redisURL,
		Password: "", // no password set
		DB:       0,  // use default DB
	})
	// ensure connected to redis
	_, err := redisCli.Ping().Result()
	if err != nil {
		err := fmt.Errorf("error: unable to ping redis at '%s'", redisURL)
		return nil, err
	}
	return redisCli, nil
}

// DashUSDRate is an entry for output to the exchange rate API
type DashUSDRate struct {
	Name      string    `json:"exchange"`
	RateUSD   float64   `json:"price"`
	VolumeUSD *float64  `json:"volume,omitempty"`
	FetchedAt time.Time `json:"fetchedAt"`
}

// MarshalBinary is part of the encoding.BinaryMarshaler interface
func (rate *DashUSDRate) MarshalBinary() ([]byte, error) {
	return json.Marshal(rate)
}

// UnmarshalBinary is part of the encoding.BinaryUnmarshaler interface
func (rate *DashUSDRate) UnmarshalBinary(data []byte) error {
	return json.Unmarshal(data, rate)
}
