use serde::{Deserialize, Serialize};
use std::{
    fs,
    path::{Path, PathBuf},
    sync::{
        Mutex,
        atomic::{AtomicBool, Ordering},
    },
};
use tauri::{
    AppHandle, Manager, WebviewWindow, WindowEvent,
    menu::{CheckMenuItem, Menu, MenuItem, PredefinedMenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
};
use tauri_plugin_autostart::ManagerExt as AutoStartExt;
use tauri_plugin_notification::{ActionPerformed, NotificationExt};

const TRAY_ID: &str = "aicli-gateway-tray";
const MENU_SHOW: &str = "tray.show";
const MENU_HIDE: &str = "tray.hide";
const MENU_HOME: &str = "tray.home";
const MENU_RELOAD: &str = "tray.reload";
const MENU_TEST_NOTIFICATION: &str = "tray.test-notification";
const MENU_AUTOSTART: &str = "tray.autostart";
const MENU_CLOSE_TO_TRAY: &str = "tray.close-to-tray";
const MENU_QUIT: &str = "tray.quit";

const SETTINGS_FILE: &str = "desktop-settings.json";

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(default)]
struct DesktopSettings {
    close_to_tray: bool,
    close_hint_shown: bool,
}

impl Default for DesktopSettings {
    fn default() -> Self {
        Self {
            close_to_tray: true,
            close_hint_shown: false,
        }
    }
}

struct DesktopState {
    settings: Mutex<DesktopSettings>,
    settings_path: PathBuf,
    quitting: AtomicBool,
    tray_available: AtomicBool,
    autostart_item: Mutex<Option<CheckMenuItem<tauri::Wry>>>,
}

// Desktop shell lifecycle: tray/menu, close-to-tray, single-instance activation,
// notification actions and lightweight persisted preferences. The gateway
// console remains the source of truth for all session/approval operations.
pub fn init(app: &mut tauri::App) -> tauri::Result<()> {
    let path = settings_path(app.handle());
    if let Some(parent) = path.parent() {
        if let Err(err) = fs::create_dir_all(parent) {
            crate::log_startup(&format!(
                "failed to create desktop settings dir {}: {err}",
                parent.display()
            ));
        }
    }
    let settings = load_settings(&path);
    crate::log_startup(&format!(
        "desktop settings: close_to_tray={} close_hint_shown={} path={}",
        settings.close_to_tray,
        settings.close_hint_shown,
        path.display()
    ));

    app.manage(DesktopState {
        settings: Mutex::new(settings),
        settings_path: path,
        quitting: AtomicBool::new(false),
        tray_available: AtomicBool::new(false),
        autostart_item: Mutex::new(None),
    });

    init_notification_actions(app.handle());

    if let Err(err) = build_tray(app.handle()) {
        crate::log_startup(&format!("failed to create system tray: {err:?}"));
    }
    Ok(())
}

pub fn start_minimized() -> bool {
    std::env::args().any(|arg| {
        matches!(
            arg.as_str(),
            "--minimized" | "--background" | "--hidden" | "--autostart"
        )
    }) || std::env::var("AICLI_START_MINIMIZED")
        .is_ok_and(|value| matches!(value.as_str(), "1" | "true" | "yes"))
}

pub fn attach_window_events(app: &AppHandle, window: &WebviewWindow) {
    let app_handle = app.clone();
    let window_handle = window.clone();
    window.on_window_event(move |event| {
        let WindowEvent::CloseRequested { api, .. } = event else {
            return;
        };
        let state = app_handle.state::<DesktopState>();
        if state.quitting.load(Ordering::SeqCst) {
            return;
        }
        let close_to_tray = state.tray_available.load(Ordering::SeqCst)
            && state
                .settings
                .lock()
                .map(|settings| settings.close_to_tray)
                .unwrap_or(true);
        if close_to_tray {
            api.prevent_close();
            if let Err(err) = window_handle.hide() {
                crate::log_startup(&format!("failed to hide window to tray: {err:?}"));
            } else {
                crate::log_startup("window hidden to system tray");
            }
            show_close_hint_once(&app_handle);
        } else {
            crate::log_startup("window close requested; exiting");
            state.quitting.store(true, Ordering::SeqCst);
            app_handle.exit(0);
        }
    });
}

pub fn show_main_window(app: &AppHandle) {
    let Some(window) = app.get_webview_window("main") else {
        crate::log_startup("show requested before main window exists");
        return;
    };
    let _ = window.unminimize();
    if let Err(err) = window.show() {
        crate::log_startup(&format!("failed to show main window: {err:?}"));
    }
    if let Err(err) = window.set_focus() {
        crate::log_startup(&format!("failed to focus main window: {err:?}"));
    }
}

pub fn hide_main_window(app: &AppHandle) {
    if let Some(window) = app.get_webview_window("main") {
        if let Err(err) = window.hide() {
            crate::log_startup(&format!("failed to hide main window: {err:?}"));
        }
    }
}

pub fn quit_app(app: &AppHandle) {
    let state = app.state::<DesktopState>();
    state.quitting.store(true, Ordering::SeqCst);
    crate::log_startup("quit requested");
    app.exit(0);
}

fn build_tray(app: &AppHandle) -> tauri::Result<()> {
    let (close_to_tray, autostart_enabled) = {
        let state = app.state::<DesktopState>();
        let close_to_tray = state
            .settings
            .lock()
            .map(|settings| settings.close_to_tray)
            .unwrap_or(true);
        (close_to_tray, query_autostart(app))
    };

    let show_item = MenuItem::with_id(app, MENU_SHOW, "显示主窗口", true, None::<&str>)?;
    let hide_item = MenuItem::with_id(app, MENU_HIDE, "隐藏到托盘", true, None::<&str>)?;
    let home_item = MenuItem::with_id(app, MENU_HOME, "返回控制台", true, None::<&str>)?;
    let reload_item = MenuItem::with_id(app, MENU_RELOAD, "重新加载控制台", true, None::<&str>)?;
    let test_item = MenuItem::with_id(
        app,
        MENU_TEST_NOTIFICATION,
        "发送测试通知",
        true,
        None::<&str>,
    )?;
    let autostart_item = CheckMenuItem::with_id(
        app,
        MENU_AUTOSTART,
        "开机启动（最小化到托盘）",
        true,
        autostart_enabled,
        None::<&str>,
    )?;
    let close_item = CheckMenuItem::with_id(
        app,
        MENU_CLOSE_TO_TRAY,
        "关闭窗口时最小化到托盘",
        true,
        close_to_tray,
        None::<&str>,
    )?;
    let quit_item = MenuItem::with_id(app, MENU_QUIT, "退出", true, None::<&str>)?;
    let separator_one = PredefinedMenuItem::separator(app)?;
    let separator_two = PredefinedMenuItem::separator(app)?;

    let menu = Menu::with_items(
        app,
        &[
            &show_item,
            &hide_item,
            &home_item,
            &reload_item,
            &test_item,
            &separator_one,
            &autostart_item,
            &close_item,
            &separator_two,
            &quit_item,
        ],
    )?;

    {
        let state = app.state::<DesktopState>();
        if let Ok(mut slot) = state.autostart_item.lock() {
            *slot = Some(autostart_item);
        }
    }

    let mut builder = TrayIconBuilder::with_id(TRAY_ID)
        .menu(&menu)
        .tooltip("aicli-gateway · 点击打开")
        .show_menu_on_left_click(false)
        .on_menu_event(|app, event| handle_menu_event(app, event.id().as_ref()))
        .on_tray_icon_event(|tray, event| match event {
            TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            }
            | TrayIconEvent::DoubleClick {
                button: MouseButton::Left,
                ..
            } => show_main_window(tray.app_handle()),
            _ => {}
        });
    if let Some(icon) = app.default_window_icon() {
        builder = builder.icon(icon.clone());
    } else {
        crate::log_startup("no default window icon for system tray");
    }
    let _tray = builder.build(app)?;
    app.state::<DesktopState>()
        .tray_available
        .store(true, Ordering::SeqCst);
    crate::log_startup("system tray ready");
    Ok(())
}

fn handle_menu_event(app: &AppHandle, id: &str) {
    match id {
        MENU_SHOW => show_main_window(app),
        MENU_HIDE => {
            hide_main_window(app);
            crate::log_startup("window hidden from tray menu");
        }
        MENU_HOME => return_to_console(app),
        MENU_RELOAD => reload_main_window(app),
        MENU_TEST_NOTIFICATION => send_test_notification(app),
        MENU_AUTOSTART => toggle_autostart(app),
        MENU_CLOSE_TO_TRAY => toggle_close_to_tray(app),
        MENU_QUIT => quit_app(app),
        _ => {}
    }
}

// return_to_console 是兜底入口：任何情况下都把主窗口带回控制台首页，
// 避免旧的“主窗口被会话页覆盖”状态无法恢复。
fn return_to_console(app: &AppHandle) {
    show_main_window(app);
    let Ok(base) = crate::gateway_url() else {
        crate::log_startup("return to console failed: gateway URL unavailable");
        return;
    };
    if let Some(window) = app.get_webview_window("main") {
        if let Err(err) = window.navigate(base) {
            crate::log_startup(&format!("return to console failed: {err:?}"));
        }
    }
}

fn reload_main_window(app: &AppHandle) {
    show_main_window(app);
    if let Some(window) = app.get_webview_window("main") {
        if let Err(err) = window.eval("window.location.reload()") {
            crate::log_startup(&format!("failed to reload main window: {err:?}"));
        }
    }
}

fn send_test_notification(app: &AppHandle) {
    let result = app
        .notification()
        .builder()
        .title("aicli-gateway")
        .body("任务栏托盘通知测试成功")
        .show();
    match result {
        Ok(()) => crate::log_startup("tray test notification sent"),
        Err(err) => crate::log_startup(&format!("tray test notification failed: {err:?}")),
    }
}

fn query_autostart(app: &AppHandle) -> bool {
    match app.autolaunch().is_enabled() {
        Ok(enabled) => enabled,
        Err(err) => {
            crate::log_startup(&format!("failed to query autostart state: {err}"));
            false
        }
    }
}

fn toggle_autostart(app: &AppHandle) {
    if tauri::is_dev() {
        crate::log_startup("autostart toggle ignored in dev build");
        set_autostart_checkbox(app, query_autostart(app));
        return;
    }
    let manager = app.autolaunch();
    let result = match manager.is_enabled() {
        Ok(true) => manager.disable().map(|()| false),
        Ok(false) => manager.enable().map(|()| true),
        Err(err) => Err(err),
    };
    match result {
        Ok(enabled) => crate::log_startup(&format!("autostart enabled={enabled}")),
        Err(err) => {
            crate::log_startup(&format!("autostart toggle failed: {err}"));
            set_autostart_checkbox(app, query_autostart(app));
        }
    }
}

fn set_autostart_checkbox(app: &AppHandle, checked: bool) {
    let state = app.state::<DesktopState>();
    if let Ok(slot) = state.autostart_item.lock() {
        if let Some(item) = slot.as_ref() {
            let _ = item.set_checked(checked);
        }
    }
}

fn toggle_close_to_tray(app: &AppHandle) {
    let state = app.state::<DesktopState>();
    let snapshot = {
        let Ok(mut settings) = state.settings.lock() else {
            return;
        };
        settings.close_to_tray = !settings.close_to_tray;
        settings.clone()
    };
    if let Err(err) = save_settings(&state.settings_path, &snapshot) {
        crate::log_startup(&format!("failed to save close-to-tray setting: {err}"));
    }
    crate::log_startup(&format!("close-to-tray enabled={}", snapshot.close_to_tray));
}

fn init_notification_actions(app: &AppHandle) {
    let app_handle = app.clone();
    if let Err(err) = app
        .notification()
        .on_action(move |action: &ActionPerformed| {
            crate::log_startup(&format!("notification action: {}", action.action_id()));
            match notification_locator(action) {
                // 桌面通知点击只做站内定位：聚焦主窗口并向控制台派发定位事件，
                // 不 navigate、不开新窗口，也不跳转会话 Web UI。
                Some((node, session)) => locate_session_in_console(&app_handle, &node, &session),
                None => show_main_window(&app_handle),
            }
        })
    {
        crate::log_startup(&format!("failed to register notification actions: {err:?}"));
    }
}

// notification_locator 从通知 extra 提取站内定位目标（node_id + session_id）。
// 桌面通知不再解析/导航到会话 Web UI；extra.url 只用于解析旧版本载荷。
fn notification_locator(action: &ActionPerformed) -> Option<(String, String)> {
    let notification = action.notification()?;
    notification_locator_from_extra(notification.extra())
}

fn notification_locator_from_extra(
    extra: &std::collections::HashMap<String, serde_json::Value>,
) -> Option<(String, String)> {
    let node = extra
        .get("node_id")
        .and_then(serde_json::Value::as_str)
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(str::to_string);
    if let Some(node) = node {
        let session = extra
            .get("session_id")
            .and_then(serde_json::Value::as_str)
            .map(str::trim)
            .unwrap_or("")
            .to_string();
        return Some((node, session));
    }
    // 旧通知可能只带 url：从 /n/{node}/web/?session=... 解析定位目标。
    let raw = extra.get("url").and_then(serde_json::Value::as_str)?;
    let url = raw.parse::<tauri::Url>().ok()?;
    let mut segments = url.path_segments()?;
    if segments.next()? != "n" {
        return None;
    }
    let node = segments.next()?.to_string();
    if node.is_empty() {
        return None;
    }
    let session = url
        .query_pairs()
        .find(|(key, _)| key == "session")
        .map(|(_, value)| value.to_string())
        .unwrap_or_default();
    Some((node, session))
}

// locate_event_script 生成注入脚本：只派发定位事件，由控制台决定 UI 行为
// （切到会话视图、清筛选、滚动并脉冲高亮），不产生任何导航。
fn locate_event_script(node_id: &str, session_id: &str) -> String {
    let detail = serde_json::json!({ "node_id": node_id, "session_id": session_id });
    format!(
        "window.dispatchEvent(new CustomEvent(\"aicli-gateway-locate\", {{ detail: {detail} }}));"
    )
}

fn locate_session_in_console(app: &AppHandle, node_id: &str, session_id: &str) {
    show_main_window(app);
    let Some(window) = app.get_webview_window("main") else {
        crate::log_startup("notification locate skipped: main window missing");
        return;
    };
    crate::log_startup(&format!(
        "notification locate: node={node_id} session={session_id}"
    ));
    if let Err(err) = window.eval(&locate_event_script(node_id, session_id)) {
        crate::log_startup(&format!("notification locate eval failed: {err:?}"));
    }
}

fn show_close_hint_once(app: &AppHandle) {
    let (path, snapshot) = {
        let state = app.state::<DesktopState>();
        let Ok(mut settings) = state.settings.lock() else {
            return;
        };
        if settings.close_hint_shown {
            return;
        }
        settings.close_hint_shown = true;
        (state.settings_path.clone(), settings.clone())
    };
    if let Err(err) = save_settings(&path, &snapshot) {
        crate::log_startup(&format!("failed to save close hint state: {err}"));
    }
    let result = app
        .notification()
        .builder()
        .title("aicli-gateway")
        .body("窗口已隐藏到任务栏托盘，通知和会话仍会继续运行。")
        .show();
    if let Err(err) = result {
        crate::log_startup(&format!("close-to-tray hint failed: {err:?}"));
    }
}

fn settings_path(app: &AppHandle) -> PathBuf {
    app.path()
        .app_config_dir()
        .unwrap_or_else(|_| std::env::temp_dir().join("aicli-gateway"))
        .join(SETTINGS_FILE)
}

fn load_settings(path: &Path) -> DesktopSettings {
    match fs::read(path) {
        Ok(data) => match serde_json::from_slice(&data) {
            Ok(settings) => settings,
            Err(err) => {
                crate::log_startup(&format!(
                    "invalid desktop settings {}: {err}; using defaults",
                    path.display()
                ));
                DesktopSettings::default()
            }
        },
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => DesktopSettings::default(),
        Err(err) => {
            crate::log_startup(&format!(
                "failed to read desktop settings {}: {err}; using defaults",
                path.display()
            ));
            DesktopSettings::default()
        }
    }
}

fn save_settings(path: &Path, settings: &DesktopSettings) -> Result<(), String> {
    let data = serde_json::to_vec_pretty(settings).map_err(|err| err.to_string())?;
    fs::write(path, data).map_err(|err| format!("write {}: {err}", path.display()))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    fn extra(value: serde_json::Value) -> HashMap<String, serde_json::Value> {
        value.as_object().unwrap().clone().into_iter().collect()
    }

    #[test]
    fn notification_locator_prefers_extra_ids() {
        let extra = extra(serde_json::json!({
            "node_id": "node-1",
            "session_id": "session-2"
        }));
        assert_eq!(
            notification_locator_from_extra(&extra),
            Some(("node-1".to_string(), "session-2".to_string()))
        );
    }

    #[test]
    fn notification_locator_parses_legacy_url() {
        let extra = extra(serde_json::json!({
            "url": "https://aicli.ttai.online/n/node-9/web/?session=session-8"
        }));
        assert_eq!(
            notification_locator_from_extra(&extra),
            Some(("node-9".to_string(), "session-8".to_string()))
        );
    }

    #[test]
    fn notification_locator_rejects_missing_target() {
        let extra = extra(serde_json::json!({
            "attention": "error"
        }));
        assert!(notification_locator_from_extra(&extra).is_none());
    }

    #[test]
    fn locate_event_script_targets_console_and_escapes_ids() {
        let script = locate_event_script("node-1", "session-\"2\"");
        assert!(script.contains("aicli-gateway-locate"));
        assert!(script.contains("node-1"));
        assert!(script.contains(r#"session-\"2\""#));
    }
}
