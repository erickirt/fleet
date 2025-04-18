import React from "react";

import { Link } from "react-router";
import paths from "router/paths";
import { ISoftwareInstallPolicy } from "interfaces/software";
import { getPathWithQueryParams } from "utilities/url";

import Modal from "components/Modal";
import Button from "components/buttons/Button";
import CustomLink from "components/CustomLink";

const baseClass = "automatic-install-modal";

interface IPoliciesListItemProps {
  teamId: number;
  policy: ISoftwareInstallPolicy;
}

const PoliciesListItem = ({ teamId, policy }: IPoliciesListItemProps) => {
  return (
    <li key={policy.id} className={`${baseClass}__list-item`}>
      <Link
        to={getPathWithQueryParams(paths.EDIT_POLICY(policy.id), {
          team_id: teamId,
        })}
      >
        {policy.name}
      </Link>
    </li>
  );
};

interface IPoliciesListProps {
  teamId: number;
  policies: ISoftwareInstallPolicy[];
}

const PoliciesList = ({ teamId, policies }: IPoliciesListProps) => {
  return (
    <ul className={`${baseClass}__list`}>
      {policies.map((policy) => (
        <PoliciesListItem key={policy.id} teamId={teamId} policy={policy} />
      ))}
    </ul>
  );
};

interface IAutomaticInstallModalProps {
  teamId: number;
  policies: ISoftwareInstallPolicy[];
  onExit: () => void;
}

const AutomaticInstallModal = ({
  teamId,
  policies,
  onExit,
}: IAutomaticInstallModalProps) => {
  const description =
    policies.length > 1 ? (
      <>
        Software will be installed when hosts fail any of these policies.{" "}
        <CustomLink
          newTab
          text="Learn more"
          url="https://fleetdm.com/learn-more-about/policy-automation-install-software"
        />
      </>
    ) : (
      <>
        Software will be installed when hosts fail this policy.{" "}
        <CustomLink
          newTab
          text="Learn more"
          url="https://fleetdm.com/learn-more-about/policy-automation-install-software"
        />
      </>
    );

  return (
    <Modal
      className={baseClass}
      title="Automatic install"
      onExit={onExit}
      width="large"
    >
      <>
        <p className={`${baseClass}__description`}>{description}</p>
        <PoliciesList teamId={teamId} policies={policies} />
        <div className="modal-cta-wrap">
          <Button onClick={onExit}>Done</Button>
        </div>
      </>
    </Modal>
  );
};

export default AutomaticInstallModal;
