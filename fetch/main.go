package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/go-redis/redis"
	"github.com/nmarley/dashrates"

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
	// fetch and store rates in Redis
	err := fetchAndStoreRates()
	if err != nil {
		return Response{StatusCode: 404}, err
	}

	var buf bytes.Buffer

	// TODO: Fetch rates from cache and return them all here...

	body, err := json.Marshal(map[string]interface{}{
		"message": "Go Serverless v1.0! Your function executed successfully!",
	})
	if err != nil {
		return Response{StatusCode: 404}, err
	}
	json.HTMLEscape(&buf, body)

	resp := Response{
		StatusCode:      200,
		IsBase64Encoded: false,
		Body:            buf.String(),
		Headers: map[string]string{
			"Content-Type":           "application/json",
			"X-MyCompany-Func-Reply": "fetch-handler",
		},
	}

	return resp, nil
}

func main() {
	lambda.Start(Handler)
}

// fetchAndStoreRates fetches exchange rates and stores them in Redis
//
// TODO: Add a channel for passing dashrates.RateInfo back to the main and
// concurrently fetch ALL rates, including the coincap one. The single wait for
// this one fetch is slowing down the entire process.
//
// Then AFTER wg.Wait() (all fetch goroutines are done executing), process the
// Dash/USD conversions and store in Redis (this takes < 30 milliseconds).
//
// main logic of this util:
//
// 1. Get BTC/USD rate first
// 2. For each exchange, pull the rate and convert to USD amounts if needed
//    (using BTC/USD rate).
// 3. Put into Redis w/an expiration
func fetchAndStoreRates() error {
	// ensure required environment variables set
	if err := envCheck([]string{"REDIS_URL"}); err != nil {
		return err
	}

	// establish redis connection
	redisCli, err := redisCliCheck(os.Getenv("REDIS_URL"))
	if err != nil {
		return err
	}

	// 1. Fetch BTC/USD rate
	coinCapAPI := dashrates.NewCoinCapAPI()
	coinCapRI, err := coinCapAPI.FetchRate()
	if err != nil {
		return err
	}

	// now we have BTC/USD rate
	rateBitcoinUSD := coinCapRI.LastPrice

	// 2. For each exchange, pull the rate and convert to USD amounts if needed
	//    (using BTC/USD rate).
	apis := []dashrates.RateAPI{
		// Coinbase is pending Dash integration (see Pro API below)
		//dashrates.NewCoinbaseAPI(),

		dashrates.NewBinanceAPI(),
		dashrates.NewKrakenAPI(),
		dashrates.NewBitfinexAPI(),
		dashrates.NewPoloniexAPI(),
		dashrates.NewHuobiAPI(),
		dashrates.NewBittrexAPI(),
		dashrates.NewLivecoinAPI(),
		dashrates.NewExmoAPI(),
		dashrates.NewHitBTCAPI(),
		dashrates.NewYobitAPI(),
		dashrates.NewCexAPI(),
		dashrates.NewBigONEAPI(),
		dashrates.NewCoinbaseProAPI(),
	}

	var wg sync.WaitGroup
	for _, rateAPI := range apis {
		wg.Add(1)
		go func(api dashrates.RateAPI) {
			defer wg.Done()
			rate, err := api.FetchRate()
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v", err.Error())
				return
			}

			usdRate, err := getDashRateInUSD(rateBitcoinUSD, api.DisplayName(), rate)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v", err.Error())
				return
			}
			fmt.Printf("rate for %s: %+v\n", api.DisplayName(), usdRate)

			// set the value w/a expiration (future calls to set will reset the
			// ttl)
			_, err = redisCli.Set(api.DisplayName(), usdRate, 24*time.Hour).Result()
			if err != nil {
				fmt.Fprintf(os.Stderr, "redis set err: %v", err.Error())
				return
			}
		}(rateAPI)
	}
	wg.Wait()
	fmt.Println("...done!")

	return nil
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

// getDashRateInUSD accepts a BTC/USD rate and a dashrates.RateInfo object and
// returns a Dash/USD rate object.
func getDashRateInUSD(rateBitcoinUSD float64, exchName string, info *dashrates.RateInfo) (*DashUSDRate, error) {
	if info.BaseCurrency != "DASH" {
		return nil, fmt.Errorf("base currency not Dash")
	}
	quoteUSD := info.LastPrice
	if info.QuoteCurrency == "BTC" {
		quoteUSD = info.LastPrice * rateBitcoinUSD
	}
	volUSD := info.BaseAssetVolume * quoteUSD

	var volPtr *float64
	if volUSD != 0 {
		volPtr = &volUSD
	}
	usdRate := &DashUSDRate{
		Name:      exchName,
		RateUSD:   quoteUSD,
		VolumeUSD: volPtr,
		FetchedAt: info.FetchTime,
	}
	return usdRate, nil
}
