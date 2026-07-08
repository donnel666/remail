const walletUpdatedEvent = "remail:wallet-updated";

export function notifyWalletUpdated() {
  if (typeof window === "undefined") return;
  window.dispatchEvent(new Event(walletUpdatedEvent));
}

export function subscribeWalletUpdated(listener: () => void) {
  if (typeof window === "undefined") return () => {};
  window.addEventListener(walletUpdatedEvent, listener);
  return () => {
    window.removeEventListener(walletUpdatedEvent, listener);
  };
}
