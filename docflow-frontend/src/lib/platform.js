// В вебе — обычный браузер, в мобильном приложении — WebView под Capacitor.
// Глобал window.Capacitor подкладывает нативный рантайм, в вебе его нет.
export function isNativeApp() {
  return !!(window.Capacitor && window.Capacitor.isNativePlatform && window.Capacitor.isNativePlatform())
}
