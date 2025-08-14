package webscan

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime/debug"
	"slack-wails/lib/gologger"
	"slack-wails/lib/structs"
	"slack-wails/lib/utils/arrayutil"
	"slack-wails/lib/utils/httputil"
	"strings"
	"sync/atomic"

	"github.com/qiwentaidi/clients"

	nuclei "github.com/projectdiscovery/nuclei/v3/lib"

	"github.com/projectdiscovery/nuclei/v3/pkg/output"
	syncutil "github.com/projectdiscovery/utils/sync"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// nuclei 最大响应包大小
var maxResponseSize = 1024 * 512

func NewNucleiEngine(ctx, ctrlCtx context.Context, taskId string, allOptions []structs.NucleiOption) {
	count := len(allOptions)
	for i, o := range allOptions {
		if ctrlCtx.Err() != nil {
			gologger.Warning(ctx, "User exits vulnerability scanning")
			return
		}
		gologger.Info(ctx, fmt.Sprintf("vulnerability scanning %d/%d", i+1, count))
		if o.SkipNucleiWithoutTags && len(o.Tags) == 0 {
			gologger.Info(ctx, fmt.Sprintf("[nuclei] %s does not have tags, scan skipped", o.URL))
			return
		}
		options := NewNucleiSDKOptions(o)
		ne, err := nuclei.NewNucleiEngineCtx(context.Background(), options...)
		if err != nil {
			gologger.DualLog(ctx, gologger.Level_ERROR, fmt.Sprintf("[nuclei] init engine err: %v", err))
			return
		}
		// load targets and optionally probe non http/https targets
		gologger.DualLog(ctx, gologger.Level_INFO, fmt.Sprintf("[nuclei] check vuln: %s", o.URL))
		ne.LoadTargets([]string{o.URL}, false)
		err = ne.ExecuteWithCallback(func(event *output.ResultEvent) {
			gologger.DualLog(ctx, gologger.Level_Success, fmt.Sprintf("[%s] [%s] %s", event.TemplateID, event.Info.SeverityHolder.Severity.String(), event.Matched))
			var reference string
			if event.Info.Reference != nil && !event.Info.Reference.IsEmpty() {
				reference = strings.Join(event.Info.Reference.ToSlice(), ",")
			}
			runtime.EventsEmit(ctx, "nucleiResult", structs.VulnerabilityInfo{
				TaskId:       taskId,
				ID:           event.TemplateID,
				Name:         event.Info.Name,
				Description:  event.Info.Description,
				Reference:    reference,
				URL:          showMatched(event),
				Request:      showRequest(event),
				Response:     showResponse(event),
				ResponseTime: limitDecimalPlaces(event.ResponseTime),
				Extract:      strings.Join(event.ExtractedResults, " | "),
				Type:         strings.ToUpper(event.Type),
				Severity:     strings.ToUpper(event.Info.SeverityHolder.Severity.String()),
			})
		})
		if err != nil {
			gologger.DualLog(ctx, gologger.Level_ERROR, fmt.Sprintf("[nuclei] execute callback err: %v", err))
			return
		}
		defer ne.Close()
		runtime.EventsEmit(ctx, "NucleiProgressID", i+1)
	}
}

func NewThreadSafeNucleiEngine(ctx, ctrlCtx context.Context, taskId string, allOptions []structs.NucleiOption) {
	count := len(allOptions)
	ne, err := nuclei.NewThreadSafeNucleiEngineCtx(context.Background())
	if err != nil {
		gologger.DualLog(ctx, gologger.Level_ERROR, fmt.Sprintf("[nuclei] init engine err: %v", err))
		return
	}
	var id int32
	sg, err := syncutil.New(syncutil.WithSize(5))
	if err != nil {
		gologger.DualLog(ctx, gologger.Level_ERROR, fmt.Sprintf("[nuclei] init sync group err: %v", err))
		return
	}
	gologger.DualLog(ctx, gologger.Level_INFO, fmt.Sprintf("[nuclei] loading %d targets to scan", count))
	ne.GlobalResultCallback(func(event *output.ResultEvent) {
		gologger.DualLog(ctx, gologger.Level_Success, fmt.Sprintf("[%s] [%s] %s", event.TemplateID, event.Info.SeverityHolder.Severity.String(), event.Matched))
		var reference string
		if event.Info.Reference != nil && !event.Info.Reference.IsEmpty() {
			reference = strings.Join(event.Info.Reference.ToSlice(), ",")
		}
		runtime.EventsEmit(ctx, "nucleiResult", structs.VulnerabilityInfo{
			TaskId:       taskId,
			ID:           event.TemplateID,
			Name:         event.Info.Name,
			Description:  event.Info.Description,
			Reference:    reference,
			URL:          showMatched(event),
			Request:      showRequest(event),
			Response:     showResponse(event),
			ResponseTime: limitDecimalPlaces(event.ResponseTime),
			Extract:      strings.Join(event.ExtractedResults, " | "),
			Type:         strings.ToUpper(event.Type),
			Severity:     strings.ToUpper(event.Info.SeverityHolder.Severity.String()),
		})
	})

	// 提交扫描任务
	for _, option := range allOptions {
		if ctrlCtx.Err() != nil {
			gologger.Warning(ctx, "User exits vulnerability scanning")
			return
		}
		sg.Add()
		// 当URL目标为WEB时，如果无指纹以及开启跳过时，则跳过该URL
		// 当URL目标为其他协议时 例如: Mysql时，不需要开启跳过，只要没有指纹就跳过
		if option.SkipNucleiWithoutTags && len(option.Tags) == 0 {
			gologger.DualLog(ctx, gologger.Level_INFO, fmt.Sprintf("[nuclei] %s does not have tags, scan skipped", option.URL))
			continue
		}
		if !strings.HasPrefix(option.URL, "http") && len(option.Tags) == 0 {
			gologger.DualLog(ctx, gologger.Level_INFO, fmt.Sprintf("[nuclei] %s is not web and does not have tags, scan skipped", option.URL))
			continue
		}
		sdkOpt := NewNucleiSDKOptions(option)
		go func(url string, Opts []nuclei.NucleiSDKOptions) {
			defer sg.Done()
			defer func() {
				if r := recover(); r != nil {
					gologger.DualLog(ctx, gologger.Level_ERROR, fmt.Sprintf("[nuclei] panic caught in goroutine: %v\n%s", r, debug.Stack()))
				}
			}()
			defer func() {
				atomic.AddInt32(&id, 1)
				runtime.EventsEmit(ctx, "NucleiProgressID", id)
				gologger.Info(ctx, fmt.Sprintf("vulnerability scanning %d/%d", id, count))
			}()

			// load targets and optionally probe non http/https targets
			gologger.DualLog(ctx, gologger.Level_INFO, fmt.Sprintf("[nuclei] check vuln: %s", url))
			err := ne.ExecuteNucleiWithOpts([]string{url}, Opts...)
			if err != nil {
				gologger.DualLog(ctx, gologger.Level_ERROR, fmt.Sprintf("[nuclei] execute callback err: %v", err))
				return
			}
		}(option.URL, sdkOpt)
	}
	sg.Wait()
	defer ne.Close()
}

func NewNucleiSDKOptions(o structs.NucleiOption) []nuclei.NucleiSDKOptions {
	options := []nuclei.NucleiSDKOptions{
		nuclei.DisableUpdateCheck(), // -duc
	}
	// 自定义请求头
	if o.CustomHeaders != "" {
		options = append(options, nuclei.WithHeaders(clients.Str2HeaderList(o.CustomHeaders)))
	}
	// 判断是使用指定poc文件还是根据标签
	if len(o.TemplateFile) == 0 {
		// fix 2.0.6: https://github.com/qiwentaidi/Slack/issues/45
		// options = append(options, nuclei.WithTemplatesOrWorkflows(nuclei.TemplateSources{
		// 	Templates: o.TemplateFolders,
		// }))

		// 如果自定义标签不为空则使用
		// options = append(options, nuclei.WithTemplateFilters(nuclei.TemplateFilters{
		// 	Tags: finalTags(o.Tags, o.CustomTags),
		// }))
		options = append(options, nuclei.WithTemplatesOrWorkflows(nuclei.TemplateSources{
			Templates: findTagsFile(finalTags(o.Tags, o.CustomTags), o.TemplateFolders),
		}))
	} else {
		// 指定poc文件的时候就要删除tags标签
		options = append(options, nuclei.WithTemplatesOrWorkflows(nuclei.TemplateSources{
			Templates: o.TemplateFile,
		}))
	}
	if o.Proxy != "" {
		options = append(options, nuclei.WithProxy([]string{o.Proxy}, false)) // -proxy
	}
	return options
}

func findTagsFile(inputTags, templateDirs []string) []string {
	if len(inputTags) == 0 {
		return expandYamlFiles(templateDirs)
	}
	var fileList []string
	var tempFileList []string
	for _, inputTag := range inputTags {
		for pocName, pocTags := range WorkFlowDB {
			if arrayutil.ArrayContains(inputTag, pocTags) {
				tempFileList = append(tempFileList, pocName)
			}
		}
	}

	for _, temp := range tempFileList {
		for _, dir := range templateDirs {
			filepath := path.Join(dir, temp+".yaml")
			if _, err := os.Stat(filepath); err == nil {
				fileList = append(fileList, filepath)
				break
			}
		}
	}
	// 如果没有找到文件，则使用指定的模板文件夹，避免使用Nuclei自带的模板文件夹
	if len(tempFileList) == 0 {
		return expandYamlFiles(templateDirs)
	}

	return arrayutil.RemoveDuplicates(fileList)
}

func expandYamlFiles(dirs []string) []string {
	var files []string
	for _, dir := range dirs {
		filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(path, ".yaml") {
				files = append(files, path)
			}
			return nil
		})
	}
	return files
}

func finalTags(detectTags, customTags []string) []string {
	if len(customTags) != 0 {
		return customTags
	}
	return detectTags
}

func showMatched(event *output.ResultEvent) string {
	if event.Matched != "" {
		return event.Matched
	}
	return event.URL
}

func showRequest(event *output.ResultEvent) string {
	if event.Request != "" {
		return event.Request
	}
	if event.Interaction != nil {
		return event.Interaction.RawRequest
	}
	return ""
}

func showResponse(event *output.ResultEvent) string {
	if event.Response != "" {
		return httputil.LimitResponse(event.Response, maxResponseSize, "")
	}
	if event.Interaction != nil {
		return event.Interaction.RawResponse
	}
	return ""
}

// 限制小数为2位，用于截取时间字符串
func limitDecimalPlaces(value string) string {
	parts := strings.Split(value, ".")
	if len(parts) == 2 && len(parts[1]) > 2 {
		value = parts[0] + "." + parts[1][:2] // 截取小数点后两位
	}
	return value
}
