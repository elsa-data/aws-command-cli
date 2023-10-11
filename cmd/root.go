package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/servicediscovery"
	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// rootCmd represents the base command when called without any subcommands
var (
	// command line flags
	namespaceFlag string
	serviceFlag   string

	rootCmd = &cobra.Command{
		Use:   "elsa-data-cli",
		Short: "An administration tool for Elsa Data instances",
		Long: `A longer description that spans multiple lines and likely contains
examples and usage of using your application. For example:
to quickly create a Cobra application.`,
		Run: func(cmd *cobra.Command, args []string) {
			//flagVal, err := cmd.Flags().GetBool("verbose")
			//if err != nil {
			//	return
			//}

			cfg, err := config.LoadDefaultConfig(context.TODO())
			if err != nil {
				log.Fatalf("unable to load AWS config, %v", err)
			}

			// all the AWS services we will use
			discoverySvc := servicediscovery.NewFromConfig(cfg)
			lambdaSvc := lambda.NewFromConfig(cfg)
			cloudWatchSvc := cloudwatchlogs.NewFromConfig(cfg)

			// discover the lambda we need to execute for CMDs
			resp, err := discoverySvc.DiscoverInstances(context.TODO(), &servicediscovery.DiscoverInstancesInput{
				NamespaceName: aws.String(namespaceFlag),
				ServiceName:   aws.String(serviceFlag),
			})

			if err != nil {
				log.Fatalf("failed to discover instances, %v", err)
			}

			if len(resp.Instances) != 1 {
				log.Fatalf("we discovered %d lambda instances in the service discovery - we need to find exactly one", len(resp.Instances))
			}

			lambdaArn, ok := resp.Instances[0].Attributes["lambdaArn"]

			if !ok {
				log.Fatalf("we discovered no lambda ARN in the service")
			}

			type LambdaCommand struct {
				Command string `json:"cmd"`
			}

			command := LambdaCommand{strings.Join(args, " ")}

			payload, err := json.Marshal(command)

			if err != nil {
				log.Fatalf("failed to construct lambda payload, %v", err)
			}

			lambdaResult, err := lambdaSvc.Invoke(context.TODO(), &lambda.InvokeInput{
				FunctionName:   aws.String(lambdaArn),
				InvocationType: "RequestResponse",
				Payload:        payload,
			})

			if err != nil {
				log.Fatalf("failed to invoke lambda, %v", err)
			}

			// this is possibly already covered by the err above
			if lambdaResult.StatusCode != 200 {
				log.Fatalf("lambda failed with status code, %v", lambdaResult.StatusCode)
			}

			// decode our result which tells us where the logs are, or an error message
			var lambdaJson map[string]any

			err = json.Unmarshal(lambdaResult.Payload, &lambdaJson)

			if err != nil {
				log.Fatalf("failed to decode lambda JSON, %v", err)
			}

			// if our returned JSON has an error field then that is a specific fatal error
			// message
			errorMessage, errorOk := lambdaJson["error"]

			if errorOk {
				log.Println(errorMessage)
				os.Exit(1)
			}

			logGroupName, logGroupNameOk := lambdaJson["logGroupName"].(string)
			logStreamName, logStreamNameOk := lambdaJson["logStreamName"].(string)

			if !logGroupNameOk || !logStreamNameOk {
				log.Fatalf("lambda invoke did not return log information to fetch")
			}

			cloudWatchPaginator := cloudwatchlogs.NewGetLogEventsPaginator(cloudWatchSvc, &cloudwatchlogs.GetLogEventsInput{
				StartFromHead: aws.Bool(true),
				LogGroupName:  aws.String(logGroupName),
				LogStreamName: aws.String(logStreamName),
			}, func(o *cloudwatchlogs.GetLogEventsPaginatorOptions) {
				o.Limit = 5
				o.StopOnDuplicateToken = true
			})

			yellow := color.New(color.FgYellow).SprintFunc()
			red := color.New(color.FgRed).SprintFunc()
			green := color.New(color.FgGreen).SprintFunc()
			blue := color.New(color.FgBlue).SprintFunc()
			cyan := color.New(color.FgCyan).SprintFunc()

			for cloudWatchPaginator.HasMorePages() {
				output, err := cloudWatchPaginator.NextPage(context.TODO())
				if err != nil {
					log.Printf("error: %v", err)
					break
				}
				for _, value := range output.Events {
					var logEventJson map[string]any

					// we get some values that we want to interpret as large integers - so want to handle
					// manually as json.Number
					d := json.NewDecoder(bytes.NewBuffer([]byte(*value.Message)))
					d.UseNumber()

					err = d.Decode(&logEventJson)

					if err != nil {
						// for any message that were print statements in Elsa Data - as opposed to logger prints...
						fmt.Printf("      %s\n", *value.Message)
					} else {
						// our cloudwatch entry is a JSON printed from the Elsa Data logger
						levelAny, hasLevel := logEventJson["level"]
						levelValueNumber, levelOk := levelAny.(json.Number)
						levelValueInt, _ := strconv.ParseInt(string(levelValueNumber), 10, 16)

						timeAny, hasTime := logEventJson["time"]
						timeValueNumber, timeOk := timeAny.(json.Number)
						timeValueInt, _ := strconv.ParseInt(string(timeValueNumber), 10, 64)

						if hasTime && timeOk {
							t := time.Unix(0, timeValueInt*int64(time.Millisecond))
							fmt.Printf(t.Format("[15:04:04.00] "))
						} else {
							fmt.Printf("           ")
						}

						if hasLevel && levelOk {
							switch levelValueInt {
							case 10:
								fmt.Printf("%s %s\n", green("TRACE"), logEventJson["msg"])
							case 20:
								fmt.Printf("%s %s\n", green("DEBUG"), logEventJson["msg"])
							case 30:
								fmt.Printf("%s %s\n", blue("INFO "), logEventJson["msg"])
							case 40:
								fmt.Printf("%s %s\n", cyan("WARN "), logEventJson["msg"])
							case 50:
								fmt.Printf("%s %s\n", yellow("ERROR"), logEventJson["msg"])
							case 60:
								fmt.Printf("%s %s\n", red("FATAL"), logEventJson["msg"])
							default:
								fmt.Printf("%s %s\n", red("?????"), logEventJson["msg"])
							}
						} else {
							fmt.Printf("     %s\n", logEventJson["msg"])
						}

						// we have a special handling for any event structures that logged a native
						// JSON object - we detect this and then pretty print the object
						if len(logEventJson) > 6 {
							delete(logEventJson, "level")
							delete(logEventJson, "msg")
							delete(logEventJson, "name")
							delete(logEventJson, "hostname")
							delete(logEventJson, "time")
							delete(logEventJson, "pid")
							data, _ := json.MarshalIndent(logEventJson, "      ", "  ")
							fmt.Println(string(data))
						}
					}
				}
			}
		},
	}
)

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&namespaceFlag, "namespace", "n",
		"elsa-data", "CloudMap namespace for Elsa Data instance")

	rootCmd.PersistentFlags().StringVarP(&serviceFlag, "service", "s",
		"Command", "CloudMap service for commands")

	// rootCmd.PersistentFlags().BoolP("verbose", "v",
	//	false, "verbose mode shows the results of all AWS calls")
}

/*birdJson := `{"birds":{"pigeon":"likes to perch on rocks","eagle":"bird of prey"},"animals":"none"}`
var result map[string]any
json.Unmarshal([]byte(birdJson), &result)

// The object stored in the "birds" key is also stored as
// a map[string]any type, and its type is asserted from
// the `any` type
birds := result["birds"].(map[string]any)

for key, value := range birds {
  // Each value is an `any` type, that is type asserted as a string
  fmt.Println(key, value.(string))
}*/
