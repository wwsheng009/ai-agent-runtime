import { CaseStudySection } from "@/components/landing/case-study-section";
import { CommunitySection } from "@/components/landing/community-section";
import { LandingFooter } from "@/components/landing/landing-footer";
import { SandboxSection } from "@/components/landing/sandbox-section";
import { SkillsSection } from "@/components/landing/skills-section";
import { WhatsNewSection } from "@/components/landing/whats-new-section";

export function LandingDeferredSections() {
  return (
    <>
      <CaseStudySection />
      <SkillsSection />
      <SandboxSection />
      <WhatsNewSection />
      <CommunitySection />
      <LandingFooter />
    </>
  );
}
