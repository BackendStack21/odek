package main

import (
	"github.com/BackendStack21/odek/internal/diagnostics"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/telegram"
)

// operationalSurfaceLogger bridges service status into the shared metadata log.
// It never copies message text or arbitrary key/value fields into records.
type operationalSurfaceLogger struct{ component string }

func newOperationalSurfaceLogger(component string) telegram.Logger {
	return operationalSurfaceLogger{component: component}
}
func (l operationalSurfaceLogger) With(fields ...any) telegram.Logger { return l }
func (l operationalSurfaceLogger) Debug(msg string, fields ...any) {
	l.emit("operation_debug", msg, fields)
}
func (l operationalSurfaceLogger) Info(msg string, fields ...any) {
	l.emit("operation_info", msg, fields)
}
func (l operationalSurfaceLogger) Warn(msg string, fields ...any) {
	l.emit("operation_warning", msg, fields)
}
func (l operationalSurfaceLogger) Error(msg string, fields ...any) {
	l.emit("operation_failed", msg, fields)
}
func (l operationalSurfaceLogger) emit(typ, msg string, fields []any) {
	operation := surfaceOperations[msg]
	if operation == "" {
		operation = "diagnostic"
	}
	data := map[string]any{"component": l.component, "operation": operation}
	for i := 0; i+1 < len(fields); i += 2 {
		if key, ok := fields[i].(string); ok && (key == "error" || key == "err") {
			if err, ok := fields[i+1].(error); ok {
				for k, v := range events.ErrorData(err) {
					data[k] = v
				}
			}
		}
	}
	diagnostics.Emit(events.Event{Type: typ, Data: data})
}

var surfaceOperations = map[string]string{
	"answer callback query (approval) failed":       "answer_callback_query_approval_failed",
	"answer callback query failed":                  "answer_callback_query_failed",
	"api error":                                     "api_error",
	"archive session":                               "archive_session",
	"auto-transcribe failed, falling back to path":  "auto_transcribe_failed_falling_back_to_path",
	"callback query handler failed":                 "callback_query_handler_failed",
	"cleaned up old media files":                    "cleaned_up_old_media_files",
	"close multipart writer failed":                 "close_multipart_writer_failed",
	"command handler failed":                        "command_handler_failed",
	"copy file content failed":                      "copy_file_content_failed",
	"create form file failed":                       "create_form_file_failed",
	"create request failed":                         "create_request_failed",
	"daily token budget exceeded":                   "daily_token_budget_exceeded",
	"daily token budget set":                        "daily_token_budget_set",
	"document download failed":                      "document_download_failed",
	"document message handler failed":               "document_message_handler_failed",
	"fatal poll error, stopping":                    "fatal_poll_error_stopping",
	"format response failed":                        "format_response_failed",
	"guard initialization failed":                   "guard_initialization_failed",
	"handler error":                                 "handler_error",
	"health server binding to non-loopback address": "health_server_binding_to_non_loopback_address",
	"health server started":                         "health_server_started",
	"http post failed":                              "http_post_failed",
	"ignoring unsupported update type":              "ignoring_unsupported_update_type",
	"marshal param failed":                          "marshal_param_failed",
	"marshal request failed":                        "marshal_request_failed",
	"media file rejected":                           "media_file_rejected",
	"media upload denied":                           "media_upload_denied",
	"media upload rejected: no approver configured": "media_upload_rejected_no_approver_configured",
	"msg":                          "msg",
	"open file failed":             "open_file_failed",
	"panic in handleChatMessage":   "panic_in_handlechatmessage",
	"panic recovered":              "panic_recovered",
	"per-turn session persist":     "per_turn_session_persist",
	"photo download failed":        "photo_download_failed",
	"photo message handler failed": "photo_message_handler_failed",
	"poll error":                   "poll_error",
	"rate limited":                 "rate_limited",
	"read response body failed":    "read_response_body_failed",
	"retrying request":             "retrying_request",
	"retrying upload":              "retrying_upload",
	"save session after cancel":    "save_session_after_cancel",
	"schedule: MCP connect failed, scheduled jobs run without MCP tools": "schedule_mcp_connect_failed_scheduled_jobs_run_without_mcp_tools",
	"schedule: drain timed out, proceeding with cleanup":                 "schedule_drain_timed_out_proceeding_with_cleanup",
	"schedule: embedded scheduler disabled by config":                    "schedule_embedded_scheduler_disabled_by_config",
	"schedule: embedded scheduler not started":                           "schedule_embedded_scheduler_not_started",
	"schedule: embedded scheduler started":                               "schedule_embedded_scheduler_started",
	"schedule: invalid default timezone, using UTC":                      "schedule_invalid_default_timezone_using_utc",
	"schedule: record delivered turn into session failed":                "schedule_record_delivered_turn_into_session_failed",
	"schedule: store unavailable, embedded scheduler not started":        "schedule_store_unavailable_embedded_scheduler_not_started",
	"schedule: store unavailable; /schedule commands degraded":           "schedule_store_unavailable_schedule_commands_degraded",
	"scheduler: delivery failed":                                         "scheduler_delivery_failed",
	"scheduler: job delivered":                                           "scheduler_job_delivered",
	"scheduler: job run failed":                                          "scheduler_job_run_failed",
	"scheduler: list jobs failed":                                        "scheduler_list_jobs_failed",
	"scheduler: load state failed":                                       "scheduler_load_state_failed",
	"scheduler: manual reload":                                           "scheduler_manual_reload",
	"scheduler: previous run still in flight, skipping this fire":        "scheduler_previous_run_still_in_flight_skipping_this_fire",
	"scheduler: save projected state failed":                             "scheduler_save_projected_state_failed",
	"scheduler: save skip state failed":                                  "scheduler_save_skip_state_failed",
	"scheduler: save state failed":                                       "scheduler_save_state_failed",
	"scheduler: schedules changed, reloading":                            "scheduler_schedules_changed_reloading",
	"scheduler: shutting down, draining in-flight jobs":                  "scheduler_shutting_down_draining_in_flight_jobs",
	"scheduler: skipping job whose cron never matches a real date":       "scheduler_skipping_job_whose_cron_never_matches_a_real_date",
	"scheduler: skipping job with invalid schedule":                      "scheduler_skipping_job_with_invalid_schedule",
	"scheduler: skipping missed fire":                                    "scheduler_skipping_missed_fire",
	"send fallback also failed":                                          "send_fallback_also_failed",
	"send media failed":                                                  "send_media_failed",
	"send message failed":                                                "send_message_failed",
	"server error":                                                       "server_error",
	"set commands failed":                                                "set_commands_failed",
	"telegram approver: expire prompt edit failed":                       "telegram_approver_expire_prompt_edit_failed",
	"telegram bot is running with NO allowlist \u2014 ANY user can drive the agent (ODEK_TELEGRAM_ALLOW_ALL=true)": "telegram_bot_is_running_with_no_allowlist_any_user_can_drive_the_agent_odek_telegram_allow_all_true",
	"telegram bot started":         "telegram_bot_started",
	"telegram voice reply skipped": "telegram_voice_reply_skipped",
	"text message handler failed":  "text_message_handler_failed",
	"unmarshal response failed":    "unmarshal_response_failed",
	"unmarshal result failed":      "unmarshal_result_failed",
	"voice download failed":        "voice_download_failed",
	"voice message handler failed": "voice_message_handler_failed",
	"write field failed":           "write_field_failed",
}
