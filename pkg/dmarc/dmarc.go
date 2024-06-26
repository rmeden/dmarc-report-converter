// Package dmarc contains reader and parser for DMARC xml reports.
package dmarc

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log"
	"math"
	"net"
	"sort"
	"time"

	"github.com/sourcegraph/conc/pool"
)

// ReportIDDateTime is the DateTime format for Report.ID
const ReportIDDateTime = "2006-01-02"

// Report represents root of dmarc report struct
type Report struct {
	XMLName         xml.Name        `xml:"feedback"`
	ReportMetadata  ReportMetadata  `xml:"report_metadata"`
	PolicyPublished PolicyPublished `xml:"policy_published"`
	Records         []Record        `xml:"record"`
	MessagesStats   MessagesStats
}

// MarshalJSON calculates report messages statistic and marshals Report struct to json, adds this
// statistic as additional fields:
//
//	"messages_stats" {
//		"all": 0,
//		"failed": 0,
//		"passed": 0,
//		"passed_percent": 0,
//	}
func (r Report) MarshalJSON() ([]byte, error) {
	r.CalculateStats()

	result := struct {
		XMLName         xml.Name        `json:"feedback"`
		ReportMetadata  ReportMetadata  `json:"report_metadata"`
		PolicyPublished PolicyPublished `json:"policy_published"`
		Records         []Record        `json:"records"`
		MessagesStats   MessagesStats   `json:"messages_stats"`
	}{
		XMLName:         r.XMLName,
		ReportMetadata:  r.ReportMetadata,
		PolicyPublished: r.PolicyPublished,
		Records:         r.Records,
		MessagesStats:   r.MessagesStats,
	}

	return json.Marshal(result)
}

// MessagesStats includes some statistic calculated from report.
type MessagesStats struct {
	// All it is the total amount of email messages
	All int `json:"all"`
	// Failed it is the total amount of failed email messages
	Failed int `json:"failed"`
	// Passed it is the total amount of passed email messages
	Passed int `json:"passed"`
	// PassedPercent it is the percent of passed email messages
	PassedPercent float64 `json:"passed_percent"`
}

// CalculateStats calculates messages statistic and updates Records.MessagesStats struct.
func (r *Report) CalculateStats() {
	s := new(MessagesStats)
	for _, record := range r.Records {
		s.All = s.All + record.Row.Count
	}

	for _, record := range r.Records {
		if record.IsPassed() {
			s.Passed = s.Passed + record.Row.Count
		}
	}
	s.Failed = s.All - s.Passed
	if s.All == 0 {
		s.PassedPercent = 0
	} else {
		s.PassedPercent = math.Round((float64(s.Passed) / float64(s.All)) * 100)
	}

	r.MessagesStats = *s
}

// SortRecords sorts records list by Row.Count
func (r *Report) SortRecords() {
	sort.Slice(r.Records, func(i, j int) bool {
		return r.Records[i].Row.Count > r.Records[j].Row.Count
	})
}

// ID returns report identifier with format YEAR-MONTH-DAY-DOMAIN/EMAIL-ID (can be used in config to
// calculate filename), where date is the begin date of report.
func (r Report) ID() string {
	d := r.ReportMetadata.DateRange.Begin.Format(ReportIDDateTime)
	return fmt.Sprintf("%v-%v/%v-%v", d, r.PolicyPublished.Domain, r.ReportMetadata.Email, r.ReportMetadata.ReportID)
}

// TodayID returns report identifier in format YEAR-MONTH-DAY-DOMAIN/EMAIL-ID (can be used in config to
// calculate filename), where date is the current date.
func (r Report) TodayID() string {
	d := time.Now().Format(ReportIDDateTime)
	return fmt.Sprintf("%v-%v/%v-%v", d, r.PolicyPublished.Domain, r.ReportMetadata.Email, r.ReportMetadata.ReportID)
}

// ReportMetadata represents feedback>report_metadata section
type ReportMetadata struct {
	OrgName          string    `xml:"org_name" json:"org_name"`
	Email            string    `xml:"email" json:"email"`
	ExtraContactInfo string    `xml:"extra_contact_info" json:"extra_contact_info"`
	ReportID         string    `xml:"report_id" json:"report_id"`
	DateRange        DateRange `xml:"date_range" json:"date_range"`
}

// DateRange represents feedback>report_metadata>date_range section
type DateRange struct {
	Begin DateTime `xml:"begin" json:"begin"`
	End   DateTime `xml:"end" json:"end"`
}

// DateTime is the custom time for DateRange.Begin and DateRange.End values
type DateTime struct {
	time.Time
}

// UnmarshalXML unmarshals unix timestamp to time.Time
func (t *DateTime) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var v int64
	d.DecodeElement(&v, &start)
	datetime := time.Unix(v, 0)
	*t = DateTime{datetime}
	return nil
}

// PolicyPublished represents feedback>policy_published section
type PolicyPublished struct {
	Domain  string `xml:"domain" json:"domain"`
	ADKIM   string `xml:"adkim" json:"adkim"`
	ASPF    string `xml:"aspf" json:"aspf"`
	Policy  string `xml:"p" json:"p"`
	SPolicy string `xml:"sp" json:"sp"`
	Pct     string `xml:"pct" json:"pct"`
}

// Record represents feedback>record section
type Record struct {
	Row         Row         `xml:"row"`
	Identifiers Identifiers `xml:"identifiers"`
	AuthResults AuthResults `xml:"auth_results"`
}

// IsPassed returns true if DKIM or SPF policies are passed
func (r Record) IsPassed() bool {
	return (r.Row.PolicyEvaluated.DKIM == "pass" || r.Row.PolicyEvaluated.SPF == "pass")
}

// MarshalJSON marshals Record struct to json, adds additional "_is_passed" field.
func (r Record) MarshalJSON() ([]byte, error) {
	result := struct {
		Row         Row         `json:"row"`
		Identifiers Identifiers `json:"identifiers"`
		AuthResults AuthResults `json:"auth_results"`
		IsPassed    bool        `json:"_is_passed"`
	}{
		Row:         r.Row,
		Identifiers: r.Identifiers,
		AuthResults: r.AuthResults,
		IsPassed:    r.IsPassed(),
	}
	return json.Marshal(result)
}

// Row represents feedback>record>row section
type Row struct {
	SourceIP        string          `xml:"source_ip" json:"source_ip"`
	Count           int             `xml:"count" json:"count"`
	PolicyEvaluated PolicyEvaluated `xml:"policy_evaluated" json:"policy_evaluated"`
	SourceHostname  string          `json:"source_hostname"`
}

// PolicyEvaluated represents feedback>record>row>policy_evaluated section
type PolicyEvaluated struct {
	Disposition string `xml:"disposition" json:"disposition"`
	DKIM        string `xml:"dkim" json:"dkim"`
	SPF         string `xml:"spf" json:"spf"`
}

// Identifiers represents feedback>record>identifiers section
type Identifiers struct {
	HeaderFrom   string `xml:"header_from" json:"header_from"`
	EnvelopeFrom string `xml:"envelope_from" json:"envelope_from"`
}

// AuthResults represents feedback>record>auth_results section
type AuthResults struct {
	DKIM []DKIMAuthResult `xml:"dkim" json:"dkim"`
	SPF  []SPFAuthResult  `xml:"spf" json:"spf"`
}

// DKIMAuthResult represnets feedback>record>auth_results>dkim sections
type DKIMAuthResult struct {
	Domain   string `xml:"domain" json:"domain"`
	Result   string `xml:"result" json:"result"`
	Selector string `xml:"selector" json:"selector"`
}

// SPFAuthResult represnets feedback>record>auth_results>spf section
type SPFAuthResult struct {
	Domain string `xml:"domain" json:"domain"`
	Result string `xml:"result" json:"result"`
	Scope  string `xml:"scope" json:"scope"`
}

// Parse parses input xml data b to Report struct.
//
// If lookupAddr is true, performs reverse DNS lookups for all
// feedback>record>row>source_ip entries.
//
// lookupLimit is the maximum pool size for doing concurrent DNS lookups. Any
// lookupLimit value less than 1 will disable concurrency by setting the pool
// size to 1.
func Parse(b []byte, lookupAddr bool, lookupLimit int) (Report, error) {
	var r Report
	err := xml.Unmarshal(b, &r)
	if err != nil {
		return Report{}, err
	}

	if lookupAddr {
		doPTRLookups(&r, lookupLimit)
	}

	r.SortRecords()
	r.CalculateStats()

	return r, nil
}

// doPTRLookups uses a limited goroutine pool to do concurrent DNS lookups for
// all record>row>source_ip entries in r.
//
// lookupLimit is the goroutine pool size. Any lookupLimit value less than 1
// will essentially disable concurrency by setting the pool size to 1.
func doPTRLookups(r *Report, lookupLimit int) {
	if lookupLimit < 1 {
		lookupLimit = 1
	}

	p := pool.New().WithMaxGoroutines(lookupLimit)

	start := time.Now()

	for i, record := range r.Records {
		i := i
		record := record

		p.Go(func() {
			hostnames, err := net.LookupAddr(record.Row.SourceIP)
			if err == nil {
				r.Records[i].Row.SourceHostname = hostnames[0]
			}
		})
	}
	log.Printf("[INFO] Parse: completed %d DNS lookups in %v for report %s", len(r.Records), time.Since(start), r.ReportMetadata.ReportID)

	p.Wait()
}
