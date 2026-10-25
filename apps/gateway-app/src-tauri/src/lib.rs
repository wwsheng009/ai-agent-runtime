mod desktop;

use std::{
    fs::{self, OpenOptions},
    io::Write,
    path::PathBuf,
    time::{SystemTime, UNIX_EPOCH},
};
use tauri::{WebviewUrl, WebviewWindowBuilder, webview::NewWindowResponse};
use tauri_plugin_window_state::StateFlags;

const DEFAULT_GATEWAY_URL: &str = "https://aicli.ttai.online/";

pub(crate) fn log_startup(message: &str) {
    let timestamp = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    eprintln!("aicli-gateway-app[{timestamp}]: {message}");

    let mut candidates = vec![std::env::temp_dir().join("aicli-gateway-app.log")];
    if let Ok(exe) = std::env::current_exe() {
        if let Some(dir) = exe.parent() {
            candidates.push(dir.join("aicli-gateway-app.log"));
        }
    }
    candidates.push(
        std::env::current_dir()
            .unwrap_or_default()
            .join("aicli-gateway-app.log"),
    );

    for path in candidates {
        if let Some(parent) = path.parent() {
            let _ = fs::create_dir_all(parent);
        }
        if let Ok(mut file) = OpenOptions::new().create(true).append(true).open(&path) {
            let _ = writeln!(file, "[{timestamp}] {message}");
            return;
        }
    }
}

fn ensure_writable_dir(path: PathBuf) -> Option<PathBuf> {
    fs::create_dir_all(&path).ok()?;
    let probe = path.join(format!(".write-test-{}", std::process::id()));
    fs::write(&probe, b"ok").ok()?;
    let _ = fs::remove_file(probe);
    Some(path)
}

fn resolve_webview_data_dir() -> Option<PathBuf> {
    if let Some(raw) = std::env::var_os("AICLI_WEBVIEW_DATA_DIR") {
        let path = PathBuf::from(raw);
        if let Some(path) = ensure_writable_dir(path.clone()) {
            return Some(path);
        }
        log_startup(&format!(
            "AICLI_WEBVIEW_DATA_DIR is not writable: {}",
            path.display()
        ));
    }

    let mut candidates = Vec::new();
    if let Some(base) = std::env::var_os("LOCALAPPDATA") {
        candidates.push(PathBuf::from(base).join("aicli-gateway").join("webview2"));
    }
    candidates.push(std::env::temp_dir().join("aicli-gateway").join("webview2"));
    if let Ok(exe) = std::env::current_exe() {
        if let Some(dir) = exe.parent() {
            candidates.push(dir.join(".webview2-data"));
        }
    }
    if let Ok(cwd) = std::env::current_dir() {
        candidates.push(cwd.join(".webview2-data"));
    }

    for path in candidates {
        if let Some(path) = ensure_writable_dir(path.clone()) {
            return Some(path);
        }
        log_startup(&format!(
            "webview data dir is not writable: {}",
            path.display()
        ));
    }
    None
}

pub(crate) fn gateway_url() -> Result<tauri::Url, String> {
    let raw =
        std::env::var("AICLI_GATEWAY_URL").unwrap_or_else(|_| DEFAULT_GATEWAY_URL.to_string());
    let normalized = format!("{}/", raw.trim().trim_end_matches('/'));
    normalized
        .parse()
        .map_err(|_| format!("AICLI_GATEWAY_URL must be an absolute http(s) URL, got {normalized}"))
}

pub(crate) fn is_allowed_navigation(url: &tauri::Url, host: &str) -> bool {
    url.host_str() == Some(host)
        && (url.scheme() == "https" || (url.scheme() == "http" && host == "localhost"))
}

// The app is intentionally a thin shell: the gateway console remains the
// single source of truth for all session/chat/approval operations. Rust only
// adds the native boundary (window, notification permission, future deep
// links). Remote navigation is restricted to the configured gateway host.
#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let previous_hook = std::panic::take_hook();
    std::panic::set_hook(Box::new(move |info| {
        log_startup(&format!("panic: {info}"));
        previous_hook(info);
    }));
    log_startup("starting");

    let mut builder = tauri::Builder::default();
    // single-instance must be the first plugin: the second process only hands
    // off argv/cwd and exits before the webview is initialized.
    #[cfg(desktop)]
    {
        builder = builder.plugin(tauri_plugin_single_instance::init(|app, argv, _cwd| {
            log_startup(&format!("second instance launched: {argv:?}"));
            if argv.iter().any(|arg| arg == "--quit") {
                desktop::quit_app(app);
            } else {
                desktop::show_main_window(app);
            }
        }));
    }
    builder = builder
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_opener::init());
    #[cfg(desktop)]
    {
        builder = builder
            .plugin(tauri_plugin_autostart::init(
                tauri_plugin_autostart::MacosLauncher::LaunchAgent,
                Some(vec!["--minimized"]),
            ))
            // Visibility is intentionally excluded: an app started with
            // --minimized must stay hidden, and hiding to tray must not make
            // the next manual launch invisible.
            .plugin(
                tauri_plugin_window_state::Builder::new()
                    .with_state_flags(
                        StateFlags::SIZE
                            | StateFlags::POSITION
                            | StateFlags::MAXIMIZED
                            | StateFlags::FULLSCREEN,
                    )
                    .build(),
            );
    }
    builder
        .setup(|app| {
            if std::env::args().any(|arg| arg == "--quit") {
                log_startup("quit requested by a first-instance command line");
                app.handle().exit(0);
                return Ok(());
            }

            #[cfg(desktop)]
            desktop::init(app)?;

            if std::env::var("AICLI_NOTIFICATION_SMOKE").is_ok_and(|v| v == "1") {
                use tauri_plugin_notification::NotificationExt;
                let _ = app
                    .notification()
                    .builder()
                    .title("aicli-gateway")
                    .body("Native notification bridge is ready")
                    .show();
            }

            let url = gateway_url().map_err(|message| {
                log_startup(&message);
                std::io::Error::new(std::io::ErrorKind::InvalidInput, message)
            })?;
            let host = url
                .host_str()
                .ok_or_else(|| {
                    let message = "AICLI_GATEWAY_URL must include a host";
                    log_startup(message);
                    std::io::Error::new(std::io::ErrorKind::InvalidInput, message)
                })?
                .to_string();
            let nav_host = host.clone();
            let new_window_host = host;

            let start_minimized = desktop::start_minimized();
            let mut builder = WebviewWindowBuilder::new(app, "main", WebviewUrl::External(url))
                .title("aicli-gateway")
                .inner_size(1280.0, 900.0)
                .min_inner_size(900.0, 600.0)
                .center()
                .visible(!start_minimized)
                .on_navigation(move |url| is_allowed_navigation(url, &nav_host))
                .on_new_window(move |url, _features| {
                    if is_allowed_navigation(&url, &new_window_host) {
                        NewWindowResponse::Allow
                    } else {
                        NewWindowResponse::Deny
                    }
                });
            if let Some(dir) = resolve_webview_data_dir() {
                log_startup(&format!("webview data dir: {}", dir.display()));
                builder = builder.data_directory(dir);
            } else {
                log_startup("no writable webview data dir; using WebView2 default");
            }
            let window = builder.build()?;
            desktop::attach_window_events(app.handle(), &window);
            if start_minimized {
                let _ = window.hide();
                log_startup("started minimized to system tray");
            } else {
                let _ = window.show();
            }
            Ok(())
        })
        .run(tauri::generate_context!())
        .unwrap_or_else(|err| {
            log_startup(&format!("tauri runtime failed: {err:?}"));
        });
}
