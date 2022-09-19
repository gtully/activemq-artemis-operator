package pods

var labelsForRh map[string]string = map[string]string{
	"com.company":   "Red_Hat",
	"rht.prod_name": "Red_Hat_Integration",
	"rht.comp":      "Broker_AMQ",
	"rht.subcomp":   "broker-amq",
	"rht.subcomp_t": "application",
}

var labelsFor7_9 map[string]string = map[string]string{
	"rht.prod_ver": "2022.Q1",
	"rht.comp_ver": "7.9.4",
}

var labelsFor7_10 map[string]string = map[string]string{
	"rht.prod_ver": "2022.Q2",
	"rht.comp_ver": "7.10.0",
}

var labelsFromVersion map[string]map[string]string = map[string]map[string]string{
	"7.9.4":  labelsFor7_9,
	"7.10.0": labelsFor7_10,
	"7.10.1": labelsFor7_10,
}

// the labels returned will be added to broker pod
func GetAdditionalLabels(fullBrokerVersion string) map[string]string {
	labels := make(map[string]string)
	for k, v := range labelsForRh {
		labels[k] = v
	}

	if versionSpecificLabels, ok := labelsFromVersion[fullBrokerVersion]; ok {
		for k, v := range versionSpecificLabels {
			labels[k] = v
		}
	} else {
		// track just image version going forward or by default
		labels["rht.comp_ver"] = fullBrokerVersion
		labels["rht.prod_ver"] = fullBrokerVersion
	}
	return labels
}
