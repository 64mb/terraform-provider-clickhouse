package common

import (
	"encoding/json"
	"fmt"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"strings"
)

func GetComment(comment string, cluster string) string {
	storingComment := fmt.Sprintf(`{"comment":"%v","cluster":"%v"}`, comment, cluster)
	storingComment = strings.Replace(storingComment, "'", "\\'", -1)
	return storingComment
}

func UnmarshalComment(storedComment string) (comment string, cluster string, err error) {
	storedComment = strings.Replace(storedComment, "\\'", "'", -1)

	byteStreamComment := []byte(storedComment)

	var dat map[string]interface{}

	if err := json.Unmarshal(byteStreamComment, &dat); err != nil {
		return "", "", err
	}
	comment = dat["comment"].(string)
	cluster = dat["cluster"].(string)

	return comment, cluster, err
}

func GetClusterStatement(cluster string) (clusterStatement string) {
	if cluster != "" {
		return "ON CLUSTER `" + strings.ReplaceAll(strings.ReplaceAll(ClusterName(cluster), "\\", "\\\\"), "`", "\\`") + "`"
	}
	return ""
}

// accept legacy SQL string literals used by the documented cluster macro configuration
func ClusterName(cluster string) string {
	if len(cluster) >= 2 && cluster[0] == '\'' && cluster[len(cluster)-1] == '\'' {
		return strings.NewReplacer("\\\\", "\\", "\\'", "'", "''", "'").Replace(cluster[1 : len(cluster)-1])
	}
	return cluster
}

// Quote all strings on a string slice
func Quote(elems []string) []string {
	var quotedElems []string
	for _, elem := range elems {
		quotedElems = append(quotedElems, fmt.Sprintf("%q", elem))
	}
	return quotedElems
}

func StringSetToList(set *schema.Set) []string {
	var list []string
	for _, item := range set.List() {
		list = append(list, item.(string))
	}
	return list
}

func StringListToSet(list []string) *schema.Set {
	var set []interface{}
	for _, item := range list {
		set = append(set, item)
	}
	return schema.NewSet(schema.HashString, set)
}
