package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"gopkg.in/yaml.v2"
)

// Config represents service configuration
// Field names should be public in order to correctly populate fields
type Config struct {
	configFile string
	Listen     string `yaml:"listen"`
	Databases  map[string]struct {
		Name     string `yaml:"name"`
		Host     string `yaml:"host"`
		Port     int    `yaml:"port"`
		User     string `yaml:"user"`
		Pass     string `yaml:"pass"`
		Database string `yaml:"database"`
	} `yaml:"databases"`
	Metrics map[string]struct {
		Db  string `yaml:"db"`
		SQL string `yaml:"sql"`
		TTL string `yaml:"ttl"`
	}
}

// Measurement represents single measurement
type Measurement struct {
	value       string
	executed    time.Time
	params      []string
	paramValues map[string]string
}

func (c *Config) getConfig() *Config {
	c.Listen = ":7070" //default listen port
	yamlFile, err := ioutil.ReadFile(c.configFile)
	if err != nil {
		log.Fatalf("Reading config: %v", err)
	}
	err = yaml.Unmarshal(yamlFile, &c)
	if err != nil {
		log.Fatalf("Unmarshal: %v", err)
	}
	return c
}

func main() {

	connectionPool := map[string]*sql.DB{}

	var c Config

	flag.StringVar(&c.configFile, "config", "config.yml", "Path to config.yml file")
	flag.Parse()

	c.getConfig()

	log.Println("Started SQL-Metric exporter")

	metrics := map[string][]Measurement{}

	for dbname, Database := range c.Databases {
		connectionString := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s", Database.User, Database.Pass, Database.Host, Database.Port, Database.Database)
		db, err := sql.Open("mysql", connectionString)
		if err != nil {
			log.Println(dbname, "=>")
			panic(err.Error())
		}
		err = db.Ping()
		if err != nil {
			panic(err.Error())
		} else {
			log.Println(dbname, "=>", "ok")
			connectionPool[dbname] = db
		}
	}

	for metricName := range c.Metrics {
		metrics[metricName] = []Measurement{{
			value:    "0",
			executed: time.Unix(0, 0),
		}}
	}

	log.Println("All connections initialized")

	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {

		for metricName, metricConfig := range c.Metrics {

			cl := []string{}

			duration, _ := time.ParseDuration(metricConfig.TTL + "s")
			if time.Now().Unix() > metrics[metricName][0].executed.Add(duration).Unix() {
				log.Println("Recalculating " + metricName)

				if strings.Contains(metricConfig.SQL, "group") {
					rows, err := connectionPool[metricConfig.Db].Query(metricConfig.SQL)
					if err != nil {
						log.Println("Error getting value: ", err.Error())
					} else {

						cl, err = rows.Columns()
						if err != nil {
							log.Println("Error getting columns: ", err.Error())
						}

						var mtA []Measurement

						// Result is your slice string.
						rawResult := make([][]byte, len(cl))

						dest := make([]interface{}, len(cl)) // A temporary interface{} slice
						for i, _ := range rawResult {
							dest[i] = &rawResult[i] // Put pointers to each string in the interface slice
						}

						for rows.Next() {
							mt := Measurement{
								value:       "",
								executed:    time.Now(),
								params:      cl[0 : len(cl)-1],
								paramValues: map[string]string{},
							}

							err = rows.Scan(dest...)
							if err != nil {
								fmt.Println("Failed to scan row", err)
								return
							}

							for i, raw := range rawResult {
								if i != len(rawResult)-1 {
									if raw == nil {
										mt.paramValues[cl[i]] = "\\N"
									} else {
										mt.paramValues[cl[i]] = string(raw)
									}
								} else {
									mt.value = string(raw)
								}
							}

							mtA = append(mtA, mt)

						}

						metrics[metricName] = mtA
						_ = rows.Close()
					}

				} else {
					row := connectionPool[metricConfig.Db].QueryRow(metricConfig.SQL)
					value := metrics[metricName][0].value
					err := row.Scan(&value)
					if err != nil {
						log.Println("Error getting value: ", err.Error())
						value = metrics[metricName][0].value
					}
					metrics[metricName][0] = Measurement{
						value:    value,
						executed: time.Now(),
					}
				}

			}

			fmt.Fprintf(w, "# TYPE %s gauge\n", metricName)
			for _, j := range metrics[metricName] {
				tags := ""
				for gg, jd := range j.paramValues {
					tags += gg + "=\"" + jd + "\","
				}
				fmt.Fprintf(w, "%s{%sdatabase=\"%s\"} %s\n", metricName, tags, metricConfig.Db, j.value)
			}

		}
	})
	log.Println("Listening for prometheus on " + c.Listen + "/metrics")
	log.Fatal(http.ListenAndServe(c.Listen, nil))
}
