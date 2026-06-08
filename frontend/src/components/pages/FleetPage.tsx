import { NodeRegistry } from '../deploy/NodeRegistry';
import { EnrollmentFlow } from '../deploy/EnrollmentFlow';

// /fleet — controller-managed node registry + enrollment flow. (Dark surface to
// match the existing components; full light/dark parity is P6.)
export function FleetPage() {
  return (
    <div className="h-full overflow-y-auto bg-gray-900 text-gray-100 p-6 space-y-6">
      <NodeRegistry />
      <EnrollmentFlow />
    </div>
  );
}
