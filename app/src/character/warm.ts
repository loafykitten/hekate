// On the very first visit the character loads before the service worker controls the page, so
// its requests miss the worker's cache and the app wouldn't open offline afterwards. Once the
// worker has taken over, ask for the same files again through it so they are cached. A page the
// worker controlled from the start loaded them through it: asking again would only download the
// model a second time (the worker refreshes what it serves in the background).
const controlledFromStart = typeof navigator !== "undefined" && !!navigator.serviceWorker?.controller;

export async function warmCharacterCache(urls: string[]): Promise<void> {
  if (!import.meta.env.PROD || !("serviceWorker" in navigator) || controlledFromStart) return;
  try {
    await navigator.serviceWorker.ready;
    if (!navigator.serviceWorker.controller) {
      await new Promise<void>((resolve) => {
        const timer = setTimeout(resolve, 10_000);
        navigator.serviceWorker.addEventListener(
          "controllerchange",
          () => {
            clearTimeout(timer);
            resolve();
          },
          { once: true },
        );
      });
    }
    if (!navigator.serviceWorker.controller) return;
    for (const url of urls) await (await fetch(url)).arrayBuffer();
  } catch {
    // Best effort: the next visit caches them anyway.
  }
}
