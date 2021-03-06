package cmd

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/dtan4/esnctl/aws"
	"github.com/dtan4/esnctl/es"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

const (
	removeMaxRetry     = 60
	removeSleepSeconds = 5
)

// removeCmd represents the remove command
var removeCmd = &cobra.Command{
	SilenceErrors: true,
	SilenceUsage:  true,
	Use:           "remove",
	Short:         "Remove node from Elasticsearch cluster",
	RunE:          doRemove,
}

var removeOpts = struct {
	autoScalingGroup string
	clusterURL       string
	nodeName         string
	region           string
}{}

func doRemove(cmd *cobra.Command, args []string) error {
	if removeOpts.clusterURL == "" {
		return errors.New("Elasticsearch cluster URL (--cluster-url) must be specified")
	}

	if removeOpts.autoScalingGroup == "" {
		return errors.New("Auto Scaling Group (--group) must be specified")
	}

	if removeOpts.nodeName == "" {
		return errors.New("Elasticsearch Node (--node-name) name must be specified")
	}

	httpClient := &http.Client{}

	client, err := es.New(removeOpts.clusterURL, httpClient)
	if err != nil {
		return errors.Wrap(err, "failed to create Elasitcsearch API client")
	}

	if err := aws.Initialize(removeOpts.region); err != nil {
		return errors.Wrap(err, "failed to initialize AWS service clients")
	}

	log.Println("===> Retrieving target instance ID...")

	instanceID, err := aws.EC2.RetrieveInstanceIDFromPrivateDNS(removeOpts.nodeName)
	if err != nil {
		return errors.Wrap(err, "failed to retrieve instance ID")
	}

	log.Println("===> Retrieving target group...")

	targetGroupARN, err := aws.AutoScaling.RetrieveTargetGroup(removeOpts.autoScalingGroup)
	if err != nil {
		return errors.Wrap(err, "failed to retrieve target group")
	}

	log.Println("===> Detaching instance from target group...")

	if err := aws.ELBv2.DetachInstance(targetGroupARN, instanceID); err != nil {
		return errors.Wrap(err, "failed to detach instance from target group")
	}

	log.Println("===> Waiting for connection draining...")

	retryCount := 0

	for {
		instances, err := aws.ELBv2.ListTargetInstances(targetGroupARN)
		if err != nil {
			return errors.Wrap(err, "failed to list instances attached to target group")
		}

		found := false

		for _, instance := range instances {
			if instance == instanceID {
				found = true
				break
			}
		}

		if !found {
			fmt.Print("\n")
			break
		}

		fmt.Print(".")

		if retryCount == removeMaxRetry {
			return errors.New("timed out: instance still remains on target group")
		}

		retryCount++
		time.Sleep(removeSleepSeconds * time.Second)
	}

	log.Println("===> Excluding target node from shard allocation group...")

	if err := client.ExcludeNodeFromAllocation(removeOpts.nodeName); err != nil {
		return errors.Wrap(err, "failed to exclude node from allocation group")
	}

	log.Println("===> Waiting for shards escape from target node...")

	retryCount = 0

	for {
		shards, err := client.ListShardsOnNode(removeOpts.nodeName)
		if err != nil {
			return errors.Wrap(err, "failed to list shards on the given node")
		}

		if len(shards) == 0 {
			fmt.Print("\n")
			break
		}

		fmt.Print(".")

		if retryCount == removeMaxRetry {
			return errors.New("timed out: shards do not escaped from the given node")
		}

		retryCount++
		time.Sleep(removeSleepSeconds * time.Second)
	}

	log.Println("===> Shutting down target node...")

	if err := client.Shutdown(removeOpts.nodeName); err != nil {
		return errors.Wrap(err, "failed to shutdown node")
	}

	log.Println("===> Detaching target instance...")

	if err := aws.AutoScaling.DetachInstance(removeOpts.autoScalingGroup, instanceID); err != nil {
		return errors.Wrap(err, "failed to detach instance from AutoScaling Group")
	}

	log.Println("===> Finished!")

	return nil
}

func init() {
	RootCmd.AddCommand(removeCmd)

	removeCmd.Flags().StringVar(&removeOpts.autoScalingGroup, "group", "", "Auto Scaling Group")
	removeCmd.Flags().StringVar(&removeOpts.clusterURL, "cluster-url", "", "Elasticsearch cluster URL")
	removeCmd.Flags().StringVar(&removeOpts.nodeName, "node-name", "", "Elasticsearch node name to remove")
	removeCmd.Flags().StringVar(&removeOpts.region, "region", "", "AWS region")
}
