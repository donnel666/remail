import type { SectionProps } from "./index";
import OrderSection from "./orders-trade";
import PaymentSection from "./payment-billing";
import ProjectPricingSection from "./project-pricing";

export default function OrdersPaymentSection(props: SectionProps) {
  return <div className="space-y-6">
    <OrderSection {...props} />
    <ProjectPricingSection {...props} />
    <PaymentSection {...props} />
  </div>;
}
