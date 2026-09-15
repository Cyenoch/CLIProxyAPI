package common

import "github.com/tidwall/gjson"

func InteractionsUsage(root gjson.Result) gjson.Result {
	for _, path := range []string{
		"interaction.usage",
		"usage",
		"metadata.total_usage",
		"metadata.usage",
		"interaction.metadata.total_usage",
		"interaction.metadata.usage",
	} {
		if value := root.Get(path); value.Exists() {
			return value
		}
	}
	return gjson.Result{}
}

func InteractionsStatus(root gjson.Result) string {
	for _, path := range []string{"interaction.status", "status"} {
		if value := root.Get(path).String(); value != "" {
			return value
		}
	}
	return ""
}

func InteractionsStopReason(root gjson.Result) string {
	for _, path := range []string{"interaction.stop_reason", "stop_reason", "metadata.stop_reason", "interaction.metadata.stop_reason"} {
		if value := root.Get(path).String(); value != "" {
			return value
		}
	}
	return ""
}
