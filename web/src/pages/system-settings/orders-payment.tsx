import type { SectionProps } from "./index";
import OrderSection from "./orders-trade";
import PaymentSection from "./payment-billing";

export default function OrdersPaymentSection(props: SectionProps) {
  return <div className="space-y-6">
    <OrderSection {...props} />
    <PaymentSection {...props} />
  </div>;
}
