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
        MENU_RELOAD => reload_main_window(app),
        MENU_TEST_NOTIFICATION => send_test_notification(app),
        MENU_AUTOSTART => toggle_autostart(app),
        MENU_CLOSE_TO_TRAY => toggle_close_to_tray(app),
        MENU_QUIT => quit_app(app),
        _ => {}
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
            show_main_window(&app_handle);
            if let Some(target) = notification_target(action) {
                if let Some(window) = app_handle.get_webview_window("main") {
                    crate::log_startup(&format!("notification deep link: {target}"));
                    if let Err(err) = window.navigate(target) {
                        crate::log_startup(&format!("notification deep link failed: {err:?}"));
                    }
                }
            }
        })
    {
        crate::log_startup(&format!("failed to register notification actions: {err:?}"));
    }
}

fn notification_target(action: &ActionPerformed) -> Option<tauri::Url> {
    let notification = action.notification()?;
    let base = crate::gateway_url().ok()?;
    notification_target_from_extra(&base, notification.extra())
}

fn notification_target_from_extra(
    base: &tauri::Url,
    extra: &std::collections::HashMap<String, serde_json::Value>,
) -> Option<tauri::Url> {
    let host = base.host_str()?.to_string();

    if let Some(raw) = extra.get("url").and_then(serde_json::Value::as_str) {
        if let Ok(candidate) = raw.parse::<tauri::Url>() {
            if crate::is_allowed_navigation(&candidate, &host) {
                return Some(candidate);
            }
        }
    }

    let node = extra
        .get("node_id")
        .and_then(serde_json::Value::as_str)
        .map(str::trim)
        .filter(|value| !value.is_empty())?;
    let session = extra
        .get("session_id")
        .and_then(serde_json::Value::as_str)
        .map(str::trim)
        .filter(|value| !value.is_empty());

    let mut target = base.clone();
    target.set_path("/");
    {
        let mut segments = target.path_segments_mut().ok()?;
        segments.clear().push("n").push(node).push("");
    }
    if let Some(session) = session {
        target.query_pairs_mut().append_pair("session", session);
    }
    Some(target)
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

    fn gateway() -> tauri::Url {
        "https://aicli.ttai.online/".parse().unwrap()
    }

    fn extra(value: serde_json::Value) -> HashMap<String, serde_json::Value> {
        value.as_object().unwrap().clone().into_iter().collect()
    }

    #[test]
    fn notification_target_builds_session_link() {
        let extra = extra(serde_json::json!({
            "node_id": "node-1",
            "session_id": "session-2"
        }));
        assert_eq!(
            notification_target_from_extra(&gateway(), &extra)
                .unwrap()
                .as_str(),
            "https://aicli.ttai.online/n/node-1/?session=session-2"
        );
    }

    #[test]
    fn notification_target_rejects_external_url() {
        let extra = extra(serde_json::json!({
            "url": "https://evil.example/session"
        }));
        assert!(notification_target_from_extra(&gateway(), &extra).is_none());
    }

    #[test]
    fn notification_target_accepts_same_gateway_url() {
        let extra = extra(serde_json::json!({
            "url": "https://aicli.ttai.online/n/node-1/?session=session-2"
        }));
        assert_eq!(
            notification_target_from_extra(&gateway(), &extra)
                .unwrap()
                .as_str(),
            "https://aicli.ttai.online/n/node-1/?session=session-2"
        );
    }
}
