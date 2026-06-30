import { createContext, useContext, type ReactNode } from "react";

interface ActivationGateValue {
  activationNeeded: boolean | null;
  markActivated: () => void;
}

const ActivationGateContext = createContext<ActivationGateValue | null>(null);

export function ActivationGateProvider({
  children,
  value,
}: {
  children: ReactNode;
  value: ActivationGateValue;
}) {
  return (
    <ActivationGateContext.Provider value={value}>
      {children}
    </ActivationGateContext.Provider>
  );
}

export function useActivationGate() {
  const value = useContext(ActivationGateContext);
  if (!value) {
    throw new Error("useActivationGate must be used within ActivationGateProvider.");
  }
  return value;
}
