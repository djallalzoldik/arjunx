package main

import (
    "bufio"
    "bytes"
    "flag"
    "fmt"
    "io"
    "log"
    "net/http"
    "net/url"
    "os"
    "strings"
    "sync"
    "time"

    "github.com/PuerkitoBio/goquery"
)

var (
    customHeadersFlag headerFlag
    proxyFlag         string
    outputFileFlag    string
    concurrencyFlag   int
    timeoutFlag       time.Duration
    quietFlag         bool
    verboseFlag       bool
    followRedirectsFlag bool
    methodFlag        string
    baseURLFlag       string
    inputFlag         string
    errorLogFlag      string
)

func init() {
    flag.Var(&customHeadersFlag, "H", "Custom header in the form 'key: value'")
    flag.StringVar(&proxyFlag, "proxy", "", "HTTP proxy in the form 'http://127.0.0.1:8080'")
    flag.StringVar(&outputFileFlag, "o", "extracted_params.txt", "Output file for extracted parameters")
    flag.IntVar(&concurrencyFlag, "c", 10, "Number of concurrent workers")
    flag.DurationVar(&timeoutFlag, "t", 30*time.Second, "HTTP request timeout")
    flag.BoolVar(&quietFlag, "q", false, "Quiet mode (suppress non-error output)")
    flag.BoolVar(&verboseFlag, "v", false, "Verbose mode (print detailed information)")
    flag.BoolVar(&followRedirectsFlag, "r", true, "Follow HTTP redirects")
    flag.StringVar(&methodFlag, "method", "GET", "HTTP request method")
    flag.StringVar(&baseURLFlag, "baseurl", "", "Base URL to prepend to input URLs")
    flag.StringVar(&inputFlag, "i", "", "Input file containing URLs to process")
    flag.StringVar(&errorLogFlag, "e", "", "Error log file")
}

type headerFlag []string

func (h *headerFlag) String() string {
    return fmt.Sprintf("%v", *h)
}

func (h *headerFlag) Set(value string) error {
    *h = append(*h, value)
    return nil
}

func main() {
    flag.Parse()

    file, err := os.Create(outputFileFlag)
    if err != nil {
        log.Fatalf("Error creating output file: %v", err)
    }
    defer file.Close()

    var errorLog *log.Logger
    if errorLogFlag != "" {
        errorLogFile, err := os.Create(errorLogFlag)
        if err != nil {
            log.Fatalf("Error creating error log file: %v", err)
        }
        defer errorLogFile.Close()
        errorLog = log.New(errorLogFile, "ERROR: ", log.LstdFlags)
    } else {
        errorLog = log.New(os.Stderr, "ERROR: ", log.LstdFlags)
    }

    urls := make(chan string, concurrencyFlag)
    var wg sync.WaitGroup

    for i := 0; i < concurrencyFlag; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for u := range urls {
                if err := processURL(u, file, customHeadersFlag, proxyFlag, timeoutFlag, quietFlag, verboseFlag, followRedirectsFlag, methodFlag, baseURLFlag, errorLog); err != nil && !quietFlag {
                    errorLog.Println(err)
                }
            }
        }()
    }

    var scanner *bufio.Scanner
    if inputFlag != "" {
        inputFile, err := os.Open(inputFlag)
        if err != nil {
            log.Fatalf("Error opening input file: %v", err)
        }
        defer inputFile.Close()
        scanner = bufio.NewScanner(inputFile)
    } else {
        scanner = bufio.NewScanner(os.Stdin)
    }

    for scanner.Scan() {
        urls <- scanner.Text()
    }
    close(urls)
    wg.Wait()

    if err := scanner.Err(); err != nil {
        log.Fatalf("Error reading input: %v", err)
    }
}

func processURL(urlStr string, file *os.File, customHeaders headerFlag, proxy string, timeout time.Duration, quiet, verbose, followRedirects bool, method, baseURL string, errorLog *log.Logger) error {
    client := &http.Client{Timeout: timeout}
    if proxy != "" {
        proxyURL, err := url.Parse(proxy)
        if err != nil {
            return fmt.Errorf("error parsing proxy URL: %v", err)
        }
        client.Transport = &http.Transport{
            Proxy: http.ProxyURL(proxyURL),
        }
    }

    if !followRedirects {
        client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
            return http.ErrUseLastResponse
        }
    }

    finalURL := urlStr
    if baseURL != "" {
        finalURL = baseURL + urlStr
    }

    req, err := http.NewRequest(method, finalURL, nil)
    if err != nil {
        return fmt.Errorf("error creating request: %v", err)
    }

    for _, header := range customHeaders {
        parts := strings.SplitN(header, ":", 2)
        if len(parts) != 2 {
            return fmt.Errorf("invalid header format: %s", header)
        }
        req.Header.Add(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
    }

    resp, err := client.Do(req)
    if err != nil {
        return fmt.Errorf("error making request to %s: %v", urlStr, err)
    }
    defer resp.Body.Close()

    buf := bytes.NewBuffer(nil)
    if _, err := io.Copy(buf, resp.Body); err != nil {
        if !quiet {
            errorLog.Printf("Error reading response body from URL %s: %v\n", urlStr, err)
        }
        return err
    }

    queryParameters := extractQueryParamsFromHTML(buf.String())

    if len(queryParameters) > 0 {
        parsedURL, err := url.Parse(urlStr)
        if err != nil {
            return fmt.Errorf("error parsing URL %s: %v", urlStr, err)
        }
        parsedURL.RawQuery = queryParameters.Encode()

        modifiedURL := parsedURL.String()
        if _, err := file.WriteString(modifiedURL + "\n"); err != nil {
            if !quiet {
                errorLog.Printf("Error writing to file: %v\n", err)
            }
            return err
        }

        if verbose {
            fmt.Printf("Extracted parameters from URL %s:\n", urlStr)
            for key, values := range queryParameters {
                fmt.Printf("%s: %s\n", key, strings.Join(values, ", "))
            }
        }
    }
    return nil
}

func extractQueryParamsFromHTML(responseBody string) url.Values {
    queryParameters := make(url.Values)

    doc, err := goquery.NewDocumentFromReader(strings.NewReader(responseBody))
    if err != nil {
        log.Printf("Error creating document from HTML: %v", err)
        return queryParameters
    }

    doc.Find("a, form, input, select, textarea").Each(func(i int, s *goquery.Selection) {
        name, exists := s.Attr("name")
        if exists {
            value := s.AttrOr("value", "FUZZ")
            queryParameters.Add(name, value)
        }

        // For `<a>` and `<form>` tags, extract URLs and parse their query parameters
        href, exists := s.Attr("href")
        if exists {
            parseURLAndAddQueryParameters(href, queryParameters)
        }

        action, exists := s.Attr("action")
        if exists {
            parseURLAndAddQueryParameters(action, queryParameters)
        }
    })

    return queryParameters
}

func parseURLAndAddQueryParameters(rawurl string, params url.Values) {
    parsedURL, err := url.Parse(rawurl)
    if err != nil {
        log.Printf("Error parsing URL %s: %v\n", rawurl, err)
        return
    }
    for key, values := range parsedURL.Query() {
        for _, value := range values {
            params.Add(key, value)
        }
    }
}
